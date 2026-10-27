package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/signatures"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type BackupInfo struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
}

type CryptoStatusInfo struct {
	DeviceID       string      `json:"device_id"`
	Fingerprint    string      `json:"fingerprint"`
	CrossSigning   bool        `json:"cross_signing"`
	Verified       bool        `json:"verified"`
	HasPrivateKeys bool        `json:"has_private_keys"`
	SecretStorage  bool        `json:"secret_storage"`
	Backup         *BackupInfo `json:"backup,omitempty"`
	BackupKey      bool        `json:"backup_key"`
	OtherDevices   int         `json:"other_devices"`
	Action         string      `json:"action"`
	Invisible      bool        `json:"invisible_crypto"`
	UTDs           int         `json:"utds"`
}

type backupKeyState struct {
	version id.KeyBackupVersion
	key     *backup.MegolmBackupKey
}

// ApprovalRequired se devuelve cuando el servidor (OAuth/MAS) exige aprobar el cambio en el navegador.
type ApprovalRequired struct {
	URL string
}

func (e *ApprovalRequired) Error() string { return "approval_required" }

func (a *Account) CryptoStatus(ctx context.Context) *CryptoStatusInfo {
	s := &CryptoStatusInfo{DeviceID: a.cli.DeviceID.String(), Invisible: a.m.Settings().InvisibleCrypto}
	if a.mach == nil {
		return s
	}
	s.Fingerprint = formatFingerprint(a.mach.OwnIdentity().SigningKey.String())
	// Aseguramos datos frescos de nuestra propia identidad (evita el "no verificado" fantasma).
	hasKeys, deviceSigned, err := a.mach.GetOwnVerificationStatus(ctx)
	if err == nil && !hasKeys {
		if _, ferr := a.mach.FetchKeys(ctx, []id.UserID{a.cli.UserID}, true); ferr == nil {
			hasKeys, deviceSigned, _ = a.mach.GetOwnVerificationStatus(ctx)
		}
	}
	s.CrossSigning = hasKeys
	if hasKeys {
		mskOK, sskOK, _, _ := a.mach.GetOwnCrossSigningVerificationStatus(ctx)
		s.HasPrivateKeys = a.mach.CrossSigningKeys != nil
		s.Verified = deviceSigned && sskOK && (mskOK || s.HasPrivateKeys)
	}
	if keyID, _ := a.mach.SSSS.GetDefaultKeyID(ctx); keyID != "" {
		s.SecretStorage = true
	}
	if v, err := a.cli.GetKeyBackupLatestVersion(ctx); err == nil && v != nil && v.Version != "" {
		s.Backup = &BackupInfo{Version: v.Version.String(), Count: v.Count}
	}
	a.backupMu.Lock()
	s.BackupKey = a.backupKey != nil && s.Backup != nil && a.backupKey.version.String() == s.Backup.Version
	a.backupMu.Unlock()
	if devs, err := a.mach.CryptoStore.GetDevices(ctx, a.cli.UserID); err == nil {
		for did := range devs {
			if did != a.cli.DeviceID {
				s.OtherDevices++
			}
		}
	}
	s.UTDs = len(a.store.AllUTDs(500))
	switch {
	case !s.CrossSigning:
		s.Action = "setup"
	case !s.Verified:
		s.Action = "verify"
	case s.Backup != nil && !s.BackupKey:
		s.Action = "backup"
	}
	return s
}

func formatFingerprint(key string) string {
	var parts []string
	for i := 0; i < len(key); i += 4 {
		end := min(i+4, len(key))
		parts = append(parts, key[i:end])
	}
	return strings.Join(parts, " ")
}

func (a *Account) uiaCallback(password string, captured **mautrix.RespUserInteractive) mautrix.UIACallback {
	return func(resp *mautrix.RespUserInteractive) any {
		*captured = resp
		for _, f := range resp.Flows {
			for _, st := range f.Stages {
				switch st {
				case mautrix.AuthTypePassword:
					if password != "" {
						return &mautrix.ReqUIAuthLogin{
							BaseAuthData: mautrix.BaseAuthData{Type: mautrix.AuthTypePassword, Session: resp.Session},
							User:         a.cli.UserID.String(),
							Password:     password,
						}
					}
				case mautrix.AuthTypeDummy:
					return &mautrix.BaseAuthData{Type: mautrix.AuthTypeDummy, Session: resp.Session}
				}
			}
		}
		return nil
	}
}

func (a *Account) approvalFromUIA(resp *mautrix.RespUserInteractive) error {
	if resp == nil {
		return nil
	}
	for _, f := range resp.Flows {
		for _, st := range f.Stages {
			if strings.Contains(string(st), "cross_signing_reset") || strings.Contains(string(st), "oauth") {
				u := gjson.Get(mustString(resp.Params), string(st)+".url").Str
				if u == "" {
					u = a.accountManagementURL("org.matrix.cross_signing_reset")
				}
				return &ApprovalRequired{URL: u}
			}
		}
	}
	return nil
}

func mustString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (a *Account) accountManagementURL(action string) string {
	meta, err := getOAuthMetadata(a.ctx, a.cli)
	if err != nil || meta == nil || meta.AccountManagementURI == "" {
		return ""
	}
	if action == "" {
		return meta.AccountManagementURI
	}
	sep := "?"
	if strings.Contains(meta.AccountManagementURI, "?") {
		sep = "&"
	}
	return meta.AccountManagementURI + sep + "action=" + action
}

// SetupRecovery crea (o reinicia) la identidad criptográfica, la clave de recuperación y el respaldo de claves.
func (a *Account) SetupRecovery(ctx context.Context, password, passphrase string) (string, error) {
	mach := a.mach
	key, err := mach.SSSS.GenerateAndUploadKey(ctx, passphrase)
	if err != nil {
		return "", fmt.Errorf("no se pudo crear la clave de recuperación: %w", err)
	}
	keys, err := mach.GenerateCrossSigningKeys()
	if err != nil {
		return "", err
	}
	var captured *mautrix.RespUserInteractive
	if err = mach.PublishCrossSigningKeys(ctx, keys, a.uiaCallback(password, &captured)); err != nil {
		if appr := a.approvalFromUIA(captured); appr != nil {
			return "", appr
		}
		if captured != nil && password == "" {
			return "", errors.New("password_required")
		}
		return "", fmt.Errorf("no se pudieron publicar las claves: %w", friendlyError(err))
	}
	if err = mach.UploadCrossSigningKeysToSSSS(ctx, key, keys); err != nil {
		return "", err
	}
	if err = mach.SSSS.SetDefaultKeyID(ctx, key.ID); err != nil {
		return "", err
	}
	if err = mach.SignOwnDevice(ctx, mach.OwnIdentity()); err != nil {
		return "", fmt.Errorf("no se pudo firmar este dispositivo: %w", err)
	}
	if err = mach.SignOwnMasterKey(ctx); err != nil {
		return "", err
	}
	for _, sec := range []struct {
		name id.Secret
		seed []byte
	}{{id.SecretXSMaster, keys.MasterKey.Seed()}, {id.SecretXSSelfSigning, keys.SelfSigningKey.Seed()}, {id.SecretXSUserSigning, keys.UserSigningKey.Seed()}} {
		_ = mach.CryptoStore.PutSecret(ctx, sec.name, base64.RawStdEncoding.EncodeToString(sec.seed))
	}
	if err = a.createBackup(ctx, key); err != nil {
		a.log.Warn().Err(err).Msg("No se pudo crear el respaldo de claves")
	}
	a.m.Broadcast(Push{Ev: "crypto.status", A: a.UserID(), D: a.CryptoStatus(ctx)})
	return key.RecoveryKey(), nil
}

func (a *Account) createBackup(ctx context.Context, ssssKey *ssss.Key) error {
	bk, err := backup.NewMegolmBackupKey()
	if err != nil {
		return err
	}
	auth := backup.MegolmAuthData{PublicKey: id.Ed25519(base64.RawStdEncoding.EncodeToString(bk.PublicKey().Bytes()))}
	sig, err := a.mach.GetAccount().SignJSON(auth)
	if err != nil {
		return err
	}
	auth.Signatures = signatures.NewSingleSignature(a.cli.UserID, id.KeyAlgorithmEd25519, a.cli.DeviceID.String(), sig)
	if a.mach.CrossSigningKeys != nil {
		if msig, err := a.mach.CrossSigningKeys.MasterKey.SignJSON(backup.MegolmAuthData{PublicKey: auth.PublicKey}); err == nil {
			mk := a.mach.CrossSigningKeys.MasterKey.PublicKey().String()
			auth.Signatures[a.cli.UserID][id.NewKeyID(id.KeyAlgorithmEd25519, mk)] = msig
		}
	}
	resp, err := a.cli.CreateKeyBackupVersion(ctx, &mautrix.ReqRoomKeysVersionCreate[backup.MegolmAuthData]{
		Algorithm: id.KeyBackupAlgorithmMegolmBackupV1, AuthData: auth,
	})
	if err != nil {
		return err
	}
	if ssssKey != nil {
		if err = a.mach.SSSS.SetEncryptedAccountData(ctx, event.AccountDataMegolmBackupKey, bk.Bytes(), ssssKey); err != nil {
			return err
		}
	}
	a.setBackupKey(ctx, resp.Version, bk)
	a.kickBackup()
	return nil
}

func (a *Account) setBackupKey(ctx context.Context, version id.KeyBackupVersion, key *backup.MegolmBackupKey) {
	a.backupMu.Lock()
	a.backupKey = &backupKeyState{version: version, key: key}
	a.backupMu.Unlock()
	_ = a.mach.CryptoStore.PutSecret(ctx, id.SecretMegolmBackupV1, base64.RawStdEncoding.EncodeToString(key.Bytes()))
	_ = a.mach.SetKeyBackupVersion(ctx, version)
}

func (a *Account) loadBackupKeyFromSSSSCache() {
	ctx := a.ctx
	secret, err := a.mach.CryptoStore.GetSecret(ctx, id.SecretMegolmBackupV1)
	if err != nil || secret == "" {
		return
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(secret, "="))
	if err != nil {
		return
	}
	key, err := backup.MegolmBackupKeyFromBytes(raw)
	if err != nil {
		return
	}
	info, err := a.mach.GetAndVerifyLatestKeyBackupVersion(ctx, key)
	if err != nil || info == nil {
		return
	}
	a.backupMu.Lock()
	a.backupKey = &backupKeyState{version: info.Version, key: key}
	a.backupMu.Unlock()
}

// RestoreWithRecoveryKey verifica este dispositivo con la clave (o frase) de recuperación y recupera el historial.
func (a *Account) RestoreWithRecoveryKey(ctx context.Context, input string) (map[string]int, error) {
	mach := a.mach
	keyID, keyData, err := mach.SSSS.GetDefaultKeyData(ctx)
	if err != nil {
		return nil, errors.New("esta cuenta no tiene una clave de recuperación configurada")
	}
	input = strings.TrimSpace(input)
	var key *ssss.Key
	key, err = keyData.VerifyRecoveryKey(keyID, input)
	if err != nil && keyData.Passphrase != nil {
		key, err = keyData.VerifyPassphrase(keyID, input)
	}
	if err != nil && !errors.Is(err, ssss.ErrUnverifiableKey) {
		return nil, errors.New("la clave de recuperación no es correcta")
	}
	if err = mach.FetchCrossSigningKeysFromSSSS(ctx, key); err != nil {
		return nil, fmt.Errorf("no se pudieron leer las claves de identidad: %w", err)
	}
	if err = mach.SignOwnDevice(ctx, mach.OwnIdentity()); err != nil {
		return nil, fmt.Errorf("no se pudo firmar este dispositivo: %w", err)
	}
	if err = mach.SignOwnMasterKey(ctx); err != nil {
		return nil, err
	}
	if seeds := mach.ExportCrossSigningKeys(); len(seeds.MasterKey) > 0 {
		_ = mach.CryptoStore.PutSecret(ctx, id.SecretXSMaster, base64.RawStdEncoding.EncodeToString(seeds.MasterKey))
		_ = mach.CryptoStore.PutSecret(ctx, id.SecretXSSelfSigning, base64.RawStdEncoding.EncodeToString(seeds.SelfSigningKey))
		_ = mach.CryptoStore.PutSecret(ctx, id.SecretXSUserSigning, base64.RawStdEncoding.EncodeToString(seeds.UserSigningKey))
	}
	result := map[string]int{}
	if raw, err := mach.SSSS.GetDecryptedAccountData(ctx, event.AccountDataMegolmBackupKey, key); err == nil {
		if bk, err := backup.MegolmBackupKeyFromBytes(raw); err == nil {
			if n, err := a.restoreBackup(ctx, bk); err == nil {
				result["imported"] = n
			} else {
				a.log.Warn().Err(err).Msg("No se pudo restaurar el respaldo")
			}
		}
	}
	_, _ = mach.FetchKeys(ctx, []id.UserID{a.cli.UserID}, true)
	go a.retryAllUTDs(5000)
	a.m.Broadcast(Push{Ev: "crypto.status", A: a.UserID(), D: a.CryptoStatus(ctx)})
	return result, nil
}

func (a *Account) restoreBackup(ctx context.Context, bk *backup.MegolmBackupKey) (int, error) {
	info, err := a.mach.GetAndVerifyLatestKeyBackupVersion(ctx, bk)
	if err != nil {
		return 0, err
	}
	if info == nil {
		return 0, errors.New("no hay respaldo de claves")
	}
	a.setBackupKey(ctx, info.Version, bk)
	if err = a.mach.GetAndStoreKeyBackup(ctx, info.Version, bk); err != nil {
		return 0, err
	}
	return info.Count, nil
}

// uploadPendingBackupKeys sube al respaldo las sesiones que aún no están respaldadas.
func (a *Account) uploadPendingBackupKeys() {
	a.backupMu.Lock()
	st := a.backupKey
	a.backupMu.Unlock()
	if st == nil {
		return
	}
	ctx := a.ctx
	store, ok := a.mach.CryptoStore.(*crypto.SQLCryptoStore)
	if !ok {
		return
	}
	sessions, err := store.GetGroupSessionsWithoutKeyBackupVersion(ctx, st.version).AsList()
	if err != nil || len(sessions) == 0 {
		return
	}
	for start := 0; start < len(sessions); start += 200 {
		chunk := sessions[start:min(start+200, len(sessions))]
		req := &mautrix.ReqKeyBackup{Rooms: map[id.RoomID]mautrix.ReqRoomKeyBackup{}}
		var ids []id.SessionID
		for _, igs := range chunk {
			exported, err := igs.Internal.Export(igs.Internal.FirstKnownIndex())
			if err != nil {
				continue
			}
			data := backup.MegolmSessionData{
				Algorithm:          id.AlgorithmMegolmV1,
				ForwardingKeyChain: igs.ForwardingChains,
				SenderClaimedKeys:  backup.SenderClaimedKeys{Ed25519: igs.SigningKey},
				SenderKey:          igs.SenderKey,
				SessionKey:         string(exported),
				SharedHistory:      igs.SharedHistory,
			}
			if data.ForwardingKeyChain == nil {
				data.ForwardingKeyChain = []string{}
			}
			enc, err := backup.EncryptSessionData(st.key, data)
			if err != nil {
				continue
			}
			encJSON, _ := json.Marshal(enc)
			room := req.Rooms[igs.RoomID]
			if room.Sessions == nil {
				room.Sessions = map[id.SessionID]mautrix.ReqKeyBackupData{}
			}
			room.Sessions[igs.ID()] = mautrix.ReqKeyBackupData{
				FirstMessageIndex: int(igs.Internal.FirstKnownIndex()),
				ForwardedCount:    len(igs.ForwardingChains),
				IsVerified:        igs.KeySource == id.KeySourceDirect,
				SessionData:       encJSON,
			}
			req.Rooms[igs.RoomID] = room
			ids = append(ids, igs.ID())
		}
		if len(ids) == 0 {
			continue
		}
		if _, err := a.cli.PutKeysInBackup(ctx, st.version, req); err != nil {
			a.log.Warn().Err(err).Msg("No se pudieron subir claves al respaldo")
			return
		}
		for _, sid := range ids {
			_ = store.SetGroupSessionKeyBackupVersion(ctx, sid, st.version)
		}
	}
}

// --- reintentos de descifrado ---

type utdState struct {
	attempts  int
	next      time.Time
	requested bool
}

// Espera progresiva entre intentos de recuperar una clave faltante.
var utdBackoff = []time.Duration{300 * time.Millisecond, 2 * time.Second, 8 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour}

func (a *Account) markUTD(sessionID string) {
	if sessionID == "" {
		return
	}
	a.utdMu.Lock()
	if _, ok := a.utdPending[sessionID]; !ok {
		a.utdPending[sessionID] = &utdState{next: time.Now().Add(utdBackoff[0])}
	}
	a.utdMu.Unlock()
}

func (a *Account) onSessionReceived(ctx context.Context, roomID id.RoomID, sessionID id.SessionID, _ uint32) {
	go a.retryDecrypt(sessionID.String())
	a.kickBackup()
}

func (a *Account) retryDecrypt(sessionID string) {
	rows := a.store.UTDsForSession(sessionID)
	if len(rows) == 0 {
		return
	}
	a.utdMu.Lock()
	delete(a.utdPending, sessionID)
	a.utdMu.Unlock()
	a.redecryptRows(rows)
}

func (a *Account) redecryptRows(rows []*EventRow) {
	ctx := a.ctx
	batch := newSyncBatch()
	for _, row := range rows {
		var evt event.Event
		if json.Unmarshal(row.Encrypted, &evt) != nil {
			continue
		}
		evt.RoomID = id.RoomID(row.RoomID)
		evt.Type.Class = event.MessageEventType
		if err := evt.Content.ParseRaw(evt.Type); err != nil {
			continue
		}
		dec, err := a.mach.DecryptMegolmEvent(ctx, &evt)
		if err != nil {
			continue
		}
		applyDecrypted(row, dec)
		fillRelations(row)
		a.store.PutEvent(row)
		a.indexRow(row)
		batch.touch(row.RoomID, row.EventID)
		if row.RelTo != "" {
			batch.touch(row.RoomID, row.RelTo)
		}
		if row.ThreadRoot != "" {
			batch.touch(row.RoomID, row.ThreadRoot)
		}
		batch.rooms[row.RoomID] = true
	}
	if len(batch.updated) > 0 {
		a.flushBatch(batch)
	}
}

func (a *Account) retryAllUTDs(limit int) {
	rows := a.store.AllUTDs(limit)
	if len(rows) > 0 {
		a.redecryptRows(rows)
	}
}

// requestMissingKeys busca las claves faltantes en el respaldo y, si no están, las pide a otros dispositivos.
func (a *Account) requestMissingKeys() {
	now := time.Now()
	a.utdMu.Lock()
	var due []string
	for sid, st := range a.utdPending {
		if now.After(st.next) {
			due = append(due, sid)
			st.attempts++
			idx := min(st.attempts, len(utdBackoff)-1)
			st.next = now.Add(utdBackoff[idx])
		}
	}
	a.utdMu.Unlock()
	if len(due) == 0 {
		return
	}
	a.backupMu.Lock()
	bk := a.backupKey
	a.backupMu.Unlock()
	for _, sid := range due {
		rows := a.store.UTDsForSession(sid)
		if len(rows) == 0 {
			a.utdMu.Lock()
			delete(a.utdPending, sid)
			a.utdMu.Unlock()
			continue
		}
		row := rows[0]
		roomID := id.RoomID(row.RoomID)
		// ¿Llegó la clave mientras tanto (p. ej. por otro camino)?
		if sess, _ := a.mach.CryptoStore.GetGroupSession(a.ctx, roomID, id.SessionID(sid)); sess != nil {
			a.retryDecrypt(sid)
			continue
		}
		if bk != nil {
			if data, err := a.cli.GetKeyBackupForRoomAndSession(a.ctx, bk.version, roomID, id.SessionID(sid)); err == nil {
				if sd, err := data.SessionData.Decrypt(bk.key); err == nil {
					if _, err = a.mach.ImportRoomKeyFromBackup(a.ctx, bk.version, roomID, id.SessionID(sid), sd); err == nil {
						a.retryDecrypt(sid)
						continue
					}
				}
			}
		}
		a.utdMu.Lock()
		st := a.utdPending[sid]
		shouldRequest := st != nil && !st.requested && st.attempts >= 2
		if shouldRequest {
			st.requested = true
		}
		a.utdMu.Unlock()
		if shouldRequest {
			enc := gjson.ParseBytes(row.Encrypted)
			senderKey := id.SenderKey(enc.Get("content.sender_key").Str)
			deviceID := id.DeviceID(enc.Get("content.device_id").Str)
			a.cli.Crypto.RequestSession(a.ctx, roomID, senderKey, id.SessionID(sid), id.UserID(row.Sender), deviceID)
		}
	}
}

// repinIdentities aplica MSC4153: si cambió la identidad criptográfica de alguien que nunca
// verificamos, aceptamos la nueva (TOFU) y avisamos; si la habíamos verificado, se bloquea
// el envío de claves hasta volver a verificar (violación de verificación).
func (a *Account) repinIdentities(ctx context.Context, roomID string, users []id.UserID) []id.UserID {
	store, ok := a.mach.CryptoStore.(*crypto.SQLCryptoStore)
	if !ok {
		return nil
	}
	var repinned []id.UserID
	own, _ := a.mach.GetOwnCrossSigningPublicKeys(ctx)
	for _, u := range users {
		if u == a.cli.UserID {
			continue
		}
		keys, err := store.GetCrossSigningKeys(ctx, u)
		if err != nil {
			continue
		}
		msk, ok := keys[id.XSUsageMaster]
		if !ok || msk.Key == msk.First {
			continue
		}
		verifiedBefore := false
		if own != nil && own.UserSigningKey != "" {
			verifiedBefore, _ = store.IsKeySignedBy(ctx, u, msk.First, a.cli.UserID, own.UserSigningKey)
		}
		if verifiedBefore {
			a.m.Broadcast(Push{Ev: "identity", A: a.UserID(), D: map[string]string{"user": u.String(), "room": roomID, "kind": "violation"}})
			continue
		}
		if err := store.ResetMasterKeyTOFU(ctx, u, msk.Key); err == nil {
			a.log.Info().Str("user", u.String()).Msg("Identidad nueva aceptada (TOFU)")
			repinned = append(repinned, u)
			a.m.Broadcast(Push{Ev: "identity", A: a.UserID(), D: map[string]string{"user": u.String(), "room": roomID, "kind": "changed"}})
		}
	}
	return repinned
}

// repinAllChanged revisa al arrancar las identidades que cambiaron mientras la app estaba cerrada.
func (a *Account) repinAllChanged() {
	rows, err := a.cryptoDB.Query(a.ctx, `SELECT user_id FROM crypto_cross_signing_keys WHERE usage='master' AND key <> first_seen_key`)
	if err != nil {
		return
	}
	var users []id.UserID
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil {
			users = append(users, id.UserID(u))
		}
	}
	rows.Close()
	a.onDeviceListsChanged(users)
}

// onDeviceListsChanged revisa cambios de identidad y rota las sesiones salientes de las salas
// compartidas, para que los dispositivos ahora confiables reciban las claves del próximo mensaje.
func (a *Account) onDeviceListsChanged(changed []id.UserID) {
	if len(changed) == 0 {
		return
	}
	ctx := a.ctx
	for _, u := range a.repinIdentities(ctx, "", changed) {
		rooms, err := a.stateStore.FindSharedRooms(ctx, u)
		if err != nil {
			continue
		}
		for _, r := range rooms {
			_ = a.mach.CryptoStore.RemoveOutboundGroupSession(ctx, r)
		}
	}
}

// AcceptIdentity acepta la nueva identidad de alguien que habíamos verificado (deja de estar verificado).
func (a *Account) AcceptIdentity(ctx context.Context, userID string) error {
	store, ok := a.mach.CryptoStore.(*crypto.SQLCryptoStore)
	if !ok {
		return errors.New("almacén no compatible")
	}
	keys, err := store.GetCrossSigningKeys(ctx, id.UserID(userID))
	if err != nil {
		return err
	}
	msk, ok := keys[id.XSUsageMaster]
	if !ok {
		return errors.New("sin identidad")
	}
	return store.ResetMasterKeyTOFU(ctx, id.UserID(userID), msk.Key)
}

// --- exportar / importar claves ---

func (a *Account) ExportKeys(ctx context.Context, passphrase string) ([]byte, error) {
	if len(passphrase) < 4 {
		return nil, errors.New("la frase de contraseña es demasiado corta")
	}
	return crypto.ExportKeysIter(passphrase, a.mach.CryptoStore.GetAllGroupSessions(ctx))
}

func (a *Account) ImportKeys(ctx context.Context, passphrase string, data []byte) (int, int, error) {
	ok, total, err := a.mach.ImportKeys(ctx, passphrase, data)
	if err == nil {
		go a.retryAllUTDs(5000)
		go a.uploadPendingBackupKeys()
	}
	return ok, total, err
}

// --- dispositivos ---

type DeviceInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	LastSeenIP  string `json:"last_seen_ip,omitempty"`
	LastSeenTS  int64  `json:"last_seen_ts,omitempty"`
	Current     bool   `json:"current,omitempty"`
	Trust       string `json:"trust"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func trustName(t id.TrustState) string {
	switch {
	case t == id.TrustStateBlacklisted:
		return "blacklisted"
	case t >= id.TrustStateCrossSignedVerified:
		return "verified"
	case t >= id.TrustStateCrossSignedTOFU:
		return "cross_signed"
	case t == id.TrustStateCrossSignedUntrusted:
		return "changed"
	default:
		return "unverified"
	}
}

func (a *Account) OwnDevices(ctx context.Context) ([]DeviceInfo, error) {
	resp, err := a.cli.GetDevicesInfo(ctx)
	if err != nil {
		return nil, err
	}
	cryptoDevs, _ := a.mach.FetchKeys(ctx, []id.UserID{a.cli.UserID}, true)
	var out []DeviceInfo
	for _, d := range resp.Devices {
		di := DeviceInfo{ID: d.DeviceID.String(), Name: d.DisplayName, LastSeenIP: d.LastSeenIP, LastSeenTS: d.LastSeenTS,
			Current: d.DeviceID == a.cli.DeviceID, Trust: "unverified"}
		var dev *id.Device
		if cryptoDevs != nil {
			dev = cryptoDevs[a.cli.UserID][d.DeviceID]
		}
		if dev == nil {
			dev, _ = a.mach.CryptoStore.GetDevice(ctx, a.cli.UserID, d.DeviceID)
		}
		if dev != nil {
			t, _ := a.mach.ResolveTrustContext(ctx, dev)
			di.Trust = trustName(t)
			di.Fingerprint = formatFingerprint(dev.SigningKey.String())
		} else {
			di.Trust = "no_crypto"
		}
		if di.Current {
			if st := a.CryptoStatus(ctx); st.Verified {
				di.Trust = "verified"
			}
		}
		out = append(out, di)
	}
	return out, nil
}

func (a *Account) UserDevices(ctx context.Context, userID string) (map[string]any, error) {
	devs, err := a.mach.FetchKeys(ctx, []id.UserID{id.UserID(userID)}, true)
	if err != nil {
		return nil, err
	}
	var list []DeviceInfo
	for _, d := range devs[id.UserID(userID)] {
		t, _ := a.mach.ResolveTrustContext(ctx, d)
		list = append(list, DeviceInfo{ID: d.DeviceID.String(), Name: d.Name, Trust: trustName(t), Fingerprint: formatFingerprint(d.SigningKey.String())})
	}
	trusted, _ := a.mach.IsUserTrusted(ctx, id.UserID(userID))
	keys, _ := a.mach.GetCrossSigningPublicKeys(ctx, id.UserID(userID))
	return map[string]any{"devices": list, "verified": trusted, "cross_signing": keys != nil}, nil
}

func (a *Account) DeleteDevices(ctx context.Context, ids []string, password string) error {
	var devs []id.DeviceID
	for _, d := range ids {
		devs = append(devs, id.DeviceID(d))
	}
	if a.Meta().LoginType == "oauth" {
		if len(ids) == 1 {
			return &ApprovalRequired{URL: a.accountManagementURL("org.matrix.device_delete") + "&device_id=" + ids[0]}
		}
		return &ApprovalRequired{URL: a.accountManagementURL("org.matrix.devices_list")}
	}
	err := a.cli.DeleteDevices(ctx, &mautrix.ReqDeleteDevices[any]{Devices: devs})
	var httpErr mautrix.HTTPError
	if errors.As(err, &httpErr) && httpErr.Response != nil && httpErr.Response.StatusCode == 401 {
		var uia mautrix.RespUserInteractive
		_ = json.Unmarshal([]byte(httpErr.ResponseBody), &uia)
		if password == "" {
			return errors.New("password_required")
		}
		err = a.cli.DeleteDevices(ctx, &mautrix.ReqDeleteDevices[any]{Devices: devs, Auth: &mautrix.ReqUIAuthLogin{
			BaseAuthData: mautrix.BaseAuthData{Type: mautrix.AuthTypePassword, Session: uia.Session},
			User:         a.cli.UserID.String(), Password: password,
		}})
	}
	return friendlyErrorNil(err)
}

func friendlyErrorNil(err error) error {
	if err == nil {
		return nil
	}
	return friendlyError(err)
}

// --- verificación interactiva ---

type verifyState struct {
	TxnID       string   `json:"txn"`
	User        string   `json:"user"`
	Device      string   `json:"device,omitempty"`
	Incoming    bool     `json:"incoming"`
	Phase       string   `json:"phase"`
	Emojis      []string `json:"emojis,omitempty"`
	EmojiNames  []string `json:"emoji_names,omitempty"`
	Decimals    []int    `json:"decimals,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	SupportsSAS bool     `json:"sas,omitempty"`
	Self        bool     `json:"self"`
	Room        string   `json:"room,omitempty"`
}

func (a *Account) setVerify(v *verifyState) {
	a.verifyMu.Lock()
	a.verifies[v.TxnID] = v
	cp := *v
	a.verifyMu.Unlock()
	a.m.Broadcast(Push{Ev: "verify", A: a.UserID(), D: cp})
}

func (a *Account) getVerify(txn string) *verifyState {
	a.verifyMu.Lock()
	defer a.verifyMu.Unlock()
	if v, ok := a.verifies[txn]; ok {
		cp := *v
		return &cp
	}
	return nil
}

type verifyCallbacks struct{ a *Account }

func (c *verifyCallbacks) VerificationRequested(ctx context.Context, txn id.VerificationTransactionID, from id.UserID, fromDevice id.DeviceID) {
	c.a.setVerify(&verifyState{TxnID: txn.String(), User: from.String(), Device: fromDevice.String(), Incoming: true,
		Phase: "requested", Self: from == c.a.cli.UserID})
	if from != c.a.cli.UserID {
		c.a.m.Notifier.Notify(c.a, NotifyItem{Title: "Solicitud de verificación", Body: from.String() + " quiere verificar su identidad contigo", Kind: "verify"})
	}
}

func (c *verifyCallbacks) VerificationReady(ctx context.Context, txn id.VerificationTransactionID, otherDevice id.DeviceID, supportsSAS, supportsScanQR bool, qr *verificationhelper.QRCode) {
	v := c.a.getVerify(txn.String())
	if v == nil {
		v = &verifyState{TxnID: txn.String()}
	}
	v.Phase, v.Device, v.SupportsSAS = "ready", otherDevice.String(), supportsSAS
	c.a.setVerify(v)
	// Quien inició la verificación arranca SAS automáticamente: un paso menos para el usuario.
	if !v.Incoming && supportsSAS {
		go func() {
			if err := c.a.verif.StartSAS(c.a.ctx, txn); err != nil {
				c.a.log.Warn().Err(err).Msg("No se pudo iniciar SAS")
			}
		}()
	}
}

func (c *verifyCallbacks) ShowSAS(ctx context.Context, txn id.VerificationTransactionID, emojis []rune, names []string, decimals []int) {
	v := c.a.getVerify(txn.String())
	if v == nil {
		v = &verifyState{TxnID: txn.String()}
	}
	v.Phase = "sas"
	v.Emojis = nil
	for _, e := range emojis {
		v.Emojis = append(v.Emojis, string(e))
	}
	v.EmojiNames, v.Decimals = names, decimals
	c.a.setVerify(v)
}

func (c *verifyCallbacks) VerificationCancelled(ctx context.Context, txn id.VerificationTransactionID, code event.VerificationCancelCode, reason string) {
	v := c.a.getVerify(txn.String())
	if v == nil {
		v = &verifyState{TxnID: txn.String()}
	}
	v.Phase, v.Reason = "cancelled", string(code)+": "+reason
	c.a.setVerify(v)
}

func (c *verifyCallbacks) VerificationDone(ctx context.Context, txn id.VerificationTransactionID, method event.VerificationMethod) {
	v := c.a.getVerify(txn.String())
	if v == nil {
		v = &verifyState{TxnID: txn.String()}
	}
	v.Phase = "done"
	c.a.setVerify(v)
	go c.a.afterVerification(v)
}

func (c *verifyCallbacks) QRCodeScanned(ctx context.Context, txn id.VerificationTransactionID) {
	v := c.a.getVerify(txn.String())
	if v == nil {
		return
	}
	v.Phase = "qr_scanned"
	c.a.setVerify(v)
}

// afterVerification: si verificamos otro dispositivo propio, pedimos los secretos
// (claves de identidad y del respaldo) para quedar completamente verificados.
func (a *Account) afterVerification(v *verifyState) {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	push := func() { a.m.Broadcast(Push{Ev: "crypto.status", A: a.UserID(), D: a.CryptoStatus(a.ctx)}) }
	devicesChanged := func() {
		a.m.Broadcast(Push{Ev: "devices.changed", A: a.UserID(), D: map[string]string{"user": v.User}})
	}
	if v.User != a.UserID() || a.mach.CrossSigningKeys != nil {
		// Verificamos a otra persona u otro dispositivo nuestro desde uno ya verificado:
		// la firma ya se subió; actualizamos claves y la interfaz enseguida.
		push()
		devicesChanged()
		_, _ = a.mach.FetchKeys(ctx, []id.UserID{a.cli.UserID, id.UserID(v.User)}, true)
		push()
		devicesChanged()
		return
	}
	// Este dispositivo acaba de ser verificado por otro nuestro: pedimos los cuatro secretos a la vez
	// (antes iban de a uno y cada pedido podía esperar hasta 30 s) y avisamos apenas los tenemos.
	push()
	decode := func(s string) []byte {
		b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
		if err != nil {
			return nil
		}
		return b
	}
	var mu sync.Mutex
	seeds := crypto.CrossSigningSeeds{}
	var wg sync.WaitGroup
	getKey := func(name id.Secret, set func([]byte)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.mach.GetOrRequestSecret(ctx, name, func(s string) (bool, error) {
				b := decode(s)
				if len(b) != 32 {
					return false, nil
				}
				mu.Lock()
				set(b)
				mu.Unlock()
				return true, nil
			}, 20*time.Second)
		}()
	}
	getKey(id.SecretXSMaster, func(b []byte) { seeds.MasterKey = b })
	getKey(id.SecretXSSelfSigning, func(b []byte) { seeds.SelfSigningKey = b })
	getKey(id.SecretXSUserSigning, func(b []byte) { seeds.UserSigningKey = b })
	backupKey := make(chan *backup.MegolmBackupKey, 1)
	go func() {
		_ = a.mach.GetOrRequestSecret(ctx, id.SecretMegolmBackupV1, func(s string) (bool, error) {
			bk, err := backup.MegolmBackupKeyFromBytes(decode(s))
			if err != nil {
				return false, nil
			}
			backupKey <- bk
			return true, nil
		}, 20*time.Second)
	}()
	// Claves de firma cruzada: con ellas este dispositivo se firma solo (no espera la firma del otro).
	wg.Wait()
	if len(seeds.SelfSigningKey) == 32 && len(seeds.MasterKey) == 32 && len(seeds.UserSigningKey) == 32 {
		if err := a.mach.ImportCrossSigningKeys(seeds); err == nil {
			_ = a.mach.SignOwnDevice(ctx, a.mach.OwnIdentity())
			_ = a.mach.SignOwnMasterKey(ctx)
		}
	}
	push()
	devicesChanged()
	// Respaldo de claves: primero se descifran los mensajes que se ven (búsqueda por sesión),
	// mientras el respaldo completo se baja de fondo.
	select {
	case bk := <-backupKey:
		info, err := a.mach.GetAndVerifyLatestKeyBackupVersion(ctx, bk)
		if err == nil && info != nil {
			a.setBackupKey(ctx, info.Version, bk)
			push()
			go a.retryAllUTDs(5000)
			if err := a.mach.GetAndStoreKeyBackup(a.ctx, info.Version, bk); err == nil {
				go a.retryAllUTDs(5000)
			}
		}
	case <-ctx.Done():
	}
	push()
}

func (a *Account) StartVerification(ctx context.Context, userID, roomID string) (string, error) {
	var txn id.VerificationTransactionID
	var err error
	if roomID != "" && userID != a.UserID() {
		txn, err = a.verif.StartInRoomVerification(ctx, id.RoomID(roomID), id.UserID(userID))
	} else {
		txn, err = a.verif.StartVerification(ctx, id.UserID(userID))
	}
	if err != nil {
		return "", err
	}
	a.setVerify(&verifyState{TxnID: txn.String(), User: userID, Phase: "requested", Self: userID == a.UserID(), Room: roomID})
	return txn.String(), nil
}

func (a *Account) VerifyAction(ctx context.Context, txn, action string) error {
	t := id.VerificationTransactionID(txn)
	switch action {
	case "accept":
		return a.verif.AcceptVerification(ctx, t)
	case "sas":
		return a.verif.StartSAS(ctx, t)
	case "confirm":
		return a.verif.ConfirmSAS(ctx, t)
	case "mismatch":
		return a.verif.CancelVerification(ctx, t, event.VerificationCancelCodeSASMismatch, "Los emojis no coinciden")
	case "cancel":
		if v := a.getVerify(txn); v != nil && v.Phase == "requested" && v.Incoming {
			_ = a.verif.DismissVerification(ctx, t)
			v.Phase = "cancelled"
			a.setVerify(v)
			return nil
		}
		return a.verif.CancelVerification(ctx, t, event.VerificationCancelCodeUser, "Cancelado por el usuario")
	}
	return fmt.Errorf("acción desconocida: %s", action)
}
