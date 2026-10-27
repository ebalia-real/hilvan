package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/pushrules"
	"maunium.net/go/mautrix/sqlstatestore"
)

type Account struct {
	m      *Manager
	metaMu sync.RWMutex
	meta   AccountMeta
	log    zerolog.Logger
	dir    string

	cli        *mautrix.Client
	store      *Store
	cryptoDB   *dbutil.Database
	stateStore *sqlstatestore.SQLStateStore
	mach       *crypto.OlmMachine
	verif      *verificationhelper.VerificationHelper
	syncer     *hSyncer

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed atomic.Bool

	status      atomic.Value // string
	statusErr   atomic.Value // string
	syncMode    atomic.Value // string: classic | sliding
	initialDone atomic.Bool

	profileMu   sync.RWMutex
	displayName string
	avatarURL   string

	dmMu    sync.RWMutex
	dmRooms map[string]string // sala -> usuario

	pushRules atomic.Pointer[pushrules.PushRuleset]

	ignoredMu sync.RWMutex
	ignored   map[string]bool

	typingMu sync.Mutex
	typing   map[string][]string

	membersMu      sync.Mutex
	membersFetches map[string]*sync.Mutex

	verifyMu sync.Mutex
	verifies map[string]*verifyState

	utdMu      sync.Mutex
	utdPending map[string]*utdState

	backupMu   sync.Mutex
	backupKey  *backupKeyState
	backupKick chan struct{}

	sendMu     sync.Mutex
	sendQueues map[string]chan *sendJob

	presenceMu sync.RWMutex
	presence   map[string]PresenceInfo

	sss *slidingSync

	proxyApplied *string
}

func randomKey(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawStdEncoding.EncodeToString(b)
}

func newAccount(m *Manager, meta AccountMeta, sec *accountSecrets) (*Account, error) {
	dir := m.accountDir(meta.UserID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	log := m.Log.With().Str("account", meta.UserID).Logger()
	ctx, cancel := context.WithCancel(m.ctx)
	ctx = log.WithContext(ctx)
	a := &Account{
		m:              m,
		meta:           meta,
		log:            log,
		dir:            dir,
		ctx:            ctx,
		cancel:         cancel,
		dmRooms:        map[string]string{},
		ignored:        map[string]bool{},
		typing:         map[string][]string{},
		membersFetches: map[string]*sync.Mutex{},
		verifies:       map[string]*verifyState{},
		utdPending:     map[string]*utdState{},
		presence:       map[string]PresenceInfo{},
		backupKick:     make(chan struct{}, 1),
		sendQueues:     map[string]chan *sendJob{},
	}
	a.status.Store("connecting")
	a.statusErr.Store("")
	a.syncMode.Store("classic")
	ok := false
	defer func() {
		if !ok {
			cancel()
			a.closeStores()
		}
	}()

	var err error
	a.store, err = OpenStore(filepath.Join(dir, "hilvan.db"))
	if err != nil {
		return nil, fmt.Errorf("base local: %w", err)
	}

	cli, err := mautrix.NewClient(meta.Homeserver, id.UserID(meta.UserID), sec.AccessToken)
	if err != nil {
		return nil, err
	}
	cli.DeviceID = id.DeviceID(meta.DeviceID)
	cli.Log = log.With().Str("component", "matrix").Logger()
	cli.UserAgent = userAgent(m.Version)
	cli.DefaultHTTPRetries = 2
	if sec.RefreshToken != "" {
		cli.OAuthSetTokens(meta.OAuthClientID, sec.RefreshToken, sec.AccessToken, time.UnixMilli(sec.TokenExpiry))
	}
	cli.SaveNewToken = func(ctx context.Context, refreshToken, accessToken string, expiry time.Time) error {
		s, err := m.loadSecrets(meta.UserID)
		if err != nil {
			s = &accountSecrets{PickleKey: sec.PickleKey}
		}
		s.AccessToken, s.RefreshToken, s.TokenExpiry = accessToken, refreshToken, expiry.UnixMilli()
		return m.saveSecrets(meta.UserID, s)
	}
	a.cli = cli

	a.cryptoDB, err = dbutil.NewWithDialect("file:"+filepath.Join(dir, "crypto.db")+"?_txlock=immediate", "sqlite3-fk-wal")
	if err != nil {
		return nil, fmt.Errorf("base de cifrado: %w", err)
	}
	dbLog := dbutil.ZeroLogger(log.With().Str("component", "db").Logger())
	a.stateStore = sqlstatestore.NewSQLStateStore(a.cryptoDB, dbLog, false)
	if err = a.stateStore.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("state store: %w", err)
	}
	cli.StateStore = a.stateStore

	pickle := []byte(sec.PickleKey)
	cryptoStore := crypto.NewSQLCryptoStore(a.cryptoDB, dbLog, meta.UserID, cli.DeviceID, pickle)
	if err = cryptoStore.DB.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("crypto store: %w", err)
	}
	cli.Store = cryptoStore

	mlog := log.With().Str("component", "crypto").Logger()
	a.mach = crypto.NewOlmMachine(cli, &mlog, cryptoStore, a.stateStore)
	a.mach.SetBackgroundCtx(ctx)
	a.mach.SessionReceived = a.onSessionReceived
	a.mach.DeleteKeysOnDeviceDelete = false
	if err = a.mach.Load(ctx); err != nil {
		return nil, fmt.Errorf("cuenta olm: %w", err)
	}
	cli.Crypto = &cryptoHelper{a: a}

	a.syncer = newHSyncer(a)
	cli.Syncer = a.syncer

	a.verif = verificationhelper.NewVerificationHelper(cli, a.mach, nil, &verifyCallbacks{a: a}, true, true, true)
	if err = a.verif.Init(ctx); err != nil {
		return nil, fmt.Errorf("verificación: %w", err)
	}
	cli.Verification = a.verif

	a.applySettings(m.Settings())
	a.loadCachedAccountData()
	a.loadProfileCache()
	ok = true
	return a, nil
}

func (a *Account) Meta() AccountMeta {
	a.metaMu.RLock()
	defer a.metaMu.RUnlock()
	return a.meta
}

func (a *Account) UserID() string { return a.meta.UserID }

func (a *Account) Client() *mautrix.Client { return a.cli }

func (a *Account) Store() *Store { return a.store }

func (a *Account) applySettings(s Settings) {
	if a.proxyApplied == nil || *a.proxyApplied != s.Proxy {
		a.cli.Client = newHTTPClient(s.Proxy, pinVerifier(a.certPins, a.onSeenCert))
		p := s.Proxy
		a.proxyApplied = &p
	}
	if s.InvisibleCrypto {
		// MSC4153: no se comparten claves con dispositivos que su dueño no verificó.
		a.mach.SendKeysMinTrust = id.TrustStateCrossSignedTOFU
	} else {
		a.mach.SendKeysMinTrust = id.TrustStateUnset
	}
	a.cli.SyncPresence = presenceFor(a.loadStatus(), s.ShowPresence)
}

func (a *Account) setStatus(status, errMsg string) {
	prev, _ := a.status.Load().(string)
	prevErr, _ := a.statusErr.Load().(string)
	a.status.Store(status)
	a.statusErr.Store(errMsg)
	if prev != status || prevErr != errMsg {
		a.m.Broadcast(Push{Ev: "account.status", A: a.UserID(), D: map[string]string{"status": status, "error": errMsg, "mode": a.syncMode.Load().(string)}})
	}
}

func (a *Account) Info() AccountInfo {
	a.profileMu.RLock()
	name, avatar := a.displayName, a.avatarURL
	a.profileMu.RUnlock()
	meta := a.Meta()
	st := a.loadStatus()
	unread, hl := 0, 0
	for _, r := range a.store.AllRooms() {
		if r.Membership != "join" {
			continue
		}
		hl += r.SrvHighlight + r.LocalHighlight
	}
	return AccountInfo{
		UserID: meta.UserID, DeviceID: meta.DeviceID, Homeserver: meta.Homeserver,
		DisplayName: name, Avatar: avatar, Status: a.status.Load().(string), SyncMode: a.syncMode.Load().(string),
		LoginType: meta.LoginType, Unread: unread, Highlight: hl,
		Presence: st.Status, StatusMsg: st.Message, StatusUntil: st.Until,
	}
}

// Run arranca el ciclo de sincronización (Sliding Sync si el servidor lo soporta).
func (a *Account) Run() {
	a.wg.Add(1)
	defer a.wg.Done()
	ctx := a.ctx
	if err := a.verifyDeviceKeysOnServer(ctx); err != nil {
		a.log.Error().Err(err).Msg("Las claves del dispositivo no coinciden con el servidor")
		if errors.Is(err, mautrix.MUnknownToken) {
			a.setStatus("logged_out", "La sesión fue cerrada en el servidor")
			return
		}
		a.setStatus("error", err.Error())
	}
	go a.refreshProfile()
	go a.fixDeviceName()
	go a.policyLoop()
	go a.retentionLoop()
	go a.historyIndexer()
	go a.statusLoop()
	go a.schedulerLoop()
	go a.resendPending()
	go a.cryptoMaintenance()

	backoff := time.Second
	for ctx.Err() == nil {
		var err error
		if a.wantSlidingSync(ctx) {
			a.syncMode.Store("sliding")
			err = a.runSlidingSync(ctx)
			if errors.Is(err, errSSSUnsupported) {
				a.log.Warn().Err(err).Msg("Sliding Sync no disponible; uso /sync clásico")
				a.store.SetKV("sss_unsupported", "1")
				continue
			}
		} else {
			a.syncMode.Store("classic")
			err = a.cli.SyncWithContext(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, mautrix.MUnknownToken) {
			a.setStatus("logged_out", "La sesión fue cerrada en el servidor")
			return
		}
		if err != nil {
			a.log.Warn().Err(err).Msg("Sincronización interrumpida")
			a.setStatus("offline", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (a *Account) Close() {
	if a.closed.Swap(true) {
		return
	}
	a.cancel()
	a.cli.StopSync()
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	a.closeStores()
}

func (a *Account) closeStores() {
	if a.mach != nil {
		_ = a.mach.FlushStore(context.Background())
	}
	if a.cryptoDB != nil {
		_ = a.cryptoDB.Close()
	}
	if a.store != nil {
		_ = a.store.Close()
	}
}

func (a *Account) verifyDeviceKeysOnServer(ctx context.Context) error {
	resp, err := a.cli.QueryKeys(ctx, &mautrix.ReqQueryKeys{
		DeviceKeys: map[id.UserID]mautrix.DeviceIDList{a.cli.UserID: {a.cli.DeviceID}},
	})
	if err != nil {
		return err
	}
	own := a.mach.OwnIdentity()
	shared := a.mach.GetAccount().Shared
	device, ok := resp.DeviceKeys[a.cli.UserID][a.cli.DeviceID]
	if !ok || len(device.Keys) == 0 {
		if shared {
			return errors.New("las claves del dispositivo desaparecieron del servidor")
		}
		return a.mach.ShareKeys(ctx, -1)
	} else if !shared {
		return errors.New("el servidor ya tiene claves para este dispositivo")
	} else if ed := device.Keys.GetEd25519(a.cli.DeviceID); own.SigningKey != ed {
		return fmt.Errorf("la clave de identidad del servidor no coincide (%s != %s)", own.SigningKey, ed)
	}
	return nil
}

// --- syncer con estado ---

type hSyncer struct {
	*mautrix.DefaultSyncer
	a *Account
}

func newHSyncer(a *Account) *hSyncer {
	ds := mautrix.NewDefaultSyncer()
	ds.FilterJSON = &mautrix.Filter{
		Room: &mautrix.RoomFilter{
			State:    &mautrix.FilterPart{LazyLoadMembers: true},
			Timeline: &mautrix.FilterPart{Limit: 40, LazyLoadMembers: true},
		},
	}
	s := &hSyncer{DefaultSyncer: ds, a: a}
	// Orden importante: primero cripto (claves to-device), luego nuestro procesamiento.
	ds.OnSync(a.mach.ProcessSyncResponse)
	ds.OnSync(a.processSync)
	ds.OnEventType(event.StateMember, a.mach.HandleMemberEvent)
	ds.OnEventType(event.StateHistoryVisibility, a.mach.HandleHistoryVisibility)
	ds.OnEvent(a.cli.StateStoreSyncHandler)
	return s
}

func (s *hSyncer) ProcessResponse(ctx context.Context, resp *mautrix.RespSync, since string) error {
	err := s.DefaultSyncer.ProcessResponse(ctx, resp, since)
	if err == nil {
		s.a.setStatus("online", "")
	}
	return err
}

func (s *hSyncer) OnFailedSync(_ *mautrix.RespSync, err error) (time.Duration, error) {
	if errors.Is(err, mautrix.MUnknownToken) {
		return 0, err
	}
	s.a.setStatus("offline", err.Error())
	return 5 * time.Second, nil
}

// --- ayudante de cifrado (implementa mautrix.CryptoHelper) ---

type cryptoHelper struct{ a *Account }

func (h *cryptoHelper) Init(context.Context) error { return nil }

func (h *cryptoHelper) Encrypt(ctx context.Context, roomID id.RoomID, evtType event.Type, content any) (*event.EncryptedEventContent, error) {
	mach := h.a.mach
	enc, err := mach.EncryptMegolmEvent(ctx, roomID, evtType, content)
	if err == nil {
		return enc, nil
	}
	if !errors.Is(err, crypto.ErrSessionExpired) && !errors.Is(err, crypto.ErrNoGroupSession) && !errors.Is(err, crypto.ErrSessionNotShared) {
		return nil, err
	}
	if err = h.a.ensureMembers(ctx, roomID.String()); err != nil {
		return nil, fmt.Errorf("no se pudo obtener la lista de miembros: %w", err)
	}
	users, err := h.a.stateStore.GetRoomJoinedOrInvitedMembers(ctx, roomID)
	if err != nil {
		return nil, err
	}
	h.a.repinIdentities(ctx, roomID.String(), users)
	if err = mach.ShareGroupSession(ctx, roomID, users); err != nil {
		return nil, fmt.Errorf("no se pudo compartir la sesión: %w", err)
	}
	return mach.EncryptMegolmEvent(ctx, roomID, evtType, content)
}

func (h *cryptoHelper) Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error) {
	return h.a.mach.DecryptMegolmEvent(ctx, evt)
}

func (h *cryptoHelper) WaitForSession(ctx context.Context, roomID id.RoomID, senderKey id.SenderKey, sessionID id.SessionID, timeout time.Duration) bool {
	return h.a.mach.WaitForSession(ctx, roomID, senderKey, sessionID, timeout)
}

func (h *cryptoHelper) RequestSession(ctx context.Context, roomID id.RoomID, senderKey id.SenderKey, sessionID id.SessionID, userID id.UserID, deviceID id.DeviceID) {
	if deviceID == "" {
		deviceID = "*"
	}
	_ = h.a.mach.SendRoomKeyRequest(ctx, roomID, senderKey, sessionID, "", map[id.UserID][]id.DeviceID{
		userID: {deviceID}, h.a.cli.UserID: {"*"},
	})
}

// ensureMembers carga la lista completa de miembros (necesaria para cifrar con lazy-loading).
func (a *Account) ensureMembers(ctx context.Context, roomID string) error {
	rid := id.RoomID(roomID)
	if fetched, _ := a.stateStore.HasFetchedMembers(ctx, rid); fetched {
		return nil
	}
	a.membersMu.Lock()
	lock, ok := a.membersFetches[roomID]
	if !ok {
		lock = &sync.Mutex{}
		a.membersFetches[roomID] = lock
	}
	a.membersMu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	if fetched, _ := a.stateStore.HasFetchedMembers(ctx, rid); fetched {
		return nil
	}
	resp, err := a.cli.Members(ctx, rid)
	if err != nil {
		return err
	}
	for _, evt := range resp.Chunk {
		_ = evt.Content.ParseRaw(evt.Type)
	}
	if err = a.stateStore.ReplaceCachedMembers(ctx, rid, resp.Chunk); err != nil {
		return err
	}
	_ = a.stateStore.MarkMembersFetched(ctx, rid)
	for _, evt := range resp.Chunk {
		if evt.StateKey == nil {
			continue
		}
		a.store.PutState(roomID, StateEntry{Type: evt.Type.Type, StateKey: *evt.StateKey, EventID: evt.ID.String(),
			Sender: evt.Sender.String(), TS: evt.Timestamp, Content: rawContent(evt)})
	}
	a.store.UpdateRoom(roomID, map[string]any{"members_loaded": 1})
	return nil
}

func rawContent(evt *event.Event) json.RawMessage {
	if len(evt.Content.VeryRaw) > 0 {
		return evt.Content.VeryRaw
	}
	b, err := json.Marshal(&evt.Content)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

func rawUnsigned(evt *event.Event) json.RawMessage {
	b, err := json.Marshal(&evt.Unsigned)
	if err != nil || string(b) == "null" {
		return json.RawMessage("{}")
	}
	return b
}

func safeName(s string) string {
	r := strings.NewReplacer("@", "", ":", "_", "/", "_", "\\", "_", "..", "_")
	return r.Replace(s)
}

// --- perfil ---

func (a *Account) loadProfileCache() {
	var p struct {
		Name   string `json:"displayname"`
		Avatar string `json:"avatar_url"`
	}
	_ = json.Unmarshal([]byte(a.store.GetKV("profile")), &p)
	a.profileMu.Lock()
	a.displayName, a.avatarURL = p.Name, p.Avatar
	a.profileMu.Unlock()
}

func (a *Account) refreshProfile() {
	prof, err := a.cli.GetProfile(a.ctx, a.cli.UserID)
	if err != nil {
		return
	}
	a.profileMu.Lock()
	a.displayName, a.avatarURL = prof.DisplayName, prof.AvatarURL.String()
	a.profileMu.Unlock()
	b, _ := json.Marshal(map[string]string{"displayname": prof.DisplayName, "avatar_url": prof.AvatarURL.String()})
	a.store.SetKV("profile", string(b))
	a.m.Broadcast(Push{Ev: "accounts", D: a.m.AccountsInfo()})
}

// cryptoMaintenance: reintentos de descifrado y subida de claves al respaldo.
func (a *Account) kickBackup() {
	select {
	case a.backupKick <- struct{}{}:
	default:
	}
}

func (a *Account) cryptoMaintenance() {
	a.wg.Add(1)
	defer a.wg.Done()
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	fast := time.NewTicker(500 * time.Millisecond)
	defer fast.Stop()
	first := time.After(4 * time.Second)
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-first:
			a.repinAllChanged()
			a.loadBackupKeyFromSSSSCache()
			a.retryAllUTDs(200)
			a.m.Broadcast(Push{Ev: "crypto.status", A: a.UserID(), D: a.CryptoStatus(a.ctx)})
		case <-a.backupKick:
			// Agrupamos ráfagas de claves nuevas antes de subirlas al respaldo.
			select {
			case <-time.After(500 * time.Millisecond):
			case <-a.ctx.Done():
				return
			}
			a.uploadPendingBackupKeys()
		case <-fast.C:
			a.requestMissingKeys()
		case <-t.C:
			a.uploadPendingBackupKeys()
		}
	}
}

// fixDeviceName pone un nombre útil a esta sesión si el servidor la dejó como "… Unknown device"
// (no toca un nombre que hayas elegido vos).
func (a *Account) fixDeviceName() {
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	info, err := a.cli.GetDeviceInfo(ctx, a.cli.DeviceID)
	if err != nil {
		return
	}
	n := info.DisplayName
	if n == "" || strings.Contains(n, "Unknown device") || n == "Hilvan" {
		if err := a.cli.SetDeviceInfo(ctx, a.cli.DeviceID, &mautrix.ReqDeviceInfo{DisplayName: deviceDisplayName()}); err == nil {
			a.log.Info().Str("nombre", deviceDisplayName()).Msg("Nombre de la sesión actualizado")
		}
	}
}
