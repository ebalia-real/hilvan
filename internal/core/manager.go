package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix"
)

// Push es un evento que el núcleo envía a la interfaz.
type Push struct {
	Ev string `json:"ev"`
	A  string `json:"a,omitempty"`
	D  any    `json:"d,omitempty"`
}

// AccountMeta se guarda en accounts.json (sin secretos).
type AccountMeta struct {
	UserID        string `json:"user_id"`
	Homeserver    string `json:"homeserver"`
	DeviceID      string `json:"device_id"`
	LoginType     string `json:"login_type"` // password | sso | oauth | token
	OAuthClientID string `json:"oauth_client_id,omitempty"`
	AddedTS       int64  `json:"added_ts"`
	Order         int    `json:"order"`
}

// accountSecrets se guarda en el llavero.
type accountSecrets struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenExpiry  int64  `json:"token_expiry,omitempty"`
	PickleKey    string `json:"pickle_key"`
}

type Focus struct {
	Account string `json:"account"`
	Room    string `json:"room"`
	Window  bool   `json:"window"` // ventana enfocada
}

type Manager struct {
	Log      zerolog.Logger
	Paths    Paths
	Version  string
	settings *settingsFile
	secrets  SecretStore

	mu       sync.RWMutex
	accounts map[string]*Account

	subsMu  sync.Mutex
	subs    map[int]chan Push
	nextSub int

	focusMu sync.RWMutex
	focus   Focus

	pendingMu sync.Mutex
	pending   map[string]*pendingLogin

	// LocalURL es la URL base del servidor local (para redirecciones SSO/OAuth).
	LocalURL string
	// OpenURL abre un enlace en el navegador del sistema (lo provee el shell).
	OpenURL func(string)
	// Quit cierra la aplicación por completo (lo provee el shell o main).
	Quit func()
	// SetBackground indica al shell si cerrar la ventana deja la app en segundo plano.
	SetBackground func(bool)
	// TrayChanged muestra u oculta el ícono de la bandeja; AppIconChanged avisa el ícono nuevo
	// (nil = el clásico); TrayAvailable dice si hay una bandeja que lo muestre.
	TrayChanged    func(bool)
	pushHook       atomic.Pointer[func(Push)]
	AppIconChanged func(png []byte)
	TrayAvailable  func() bool
	// PickSavePath abre el diálogo "Guardar como" de la ventana (devuelve "" si se cancela).
	// Sin ventana se usa el selector del sistema (portal).
	PickSavePath func(title, name, folder string) (string, error)
	// ExportFile copia un archivo ya guardado (src) a un destino del sistema (Android: una URI
	// content:// elegida, o "downloads:" para la carpeta Descargas). Devuelve dónde quedó.
	ExportFile func(src, dest, name, mime string) (string, error)
	// Android: LauncherIcon cambia el ícono del lanzador (estilo), OnSettings aplica los ajustes
	// que maneja el sistema (segundo plano, inicio) y Platform hace acciones propias (batería…).
	LauncherIcon func(style string)
	// InstallPackage instala una actualización ya verificada (Android: el instalador del sistema).
	InstallPackage func(path string) error
	upd            updater
	OnSettings     func(Settings)
	Platform       func(action string) string
	// SetDark cambia el tema de la ventana nativa (1 oscuro, 0 claro, -1 sistema).
	SetDark func(int)
	// OnActivate trae la ventana al frente (lo provee el shell).
	OnActivate func()
	// Notifier muestra notificaciones de escritorio.
	Notifier Notifier
	// Calls maneja las llamadas 1:1 y grupales.
	Calls *CallManager
	// Video muestra el video nativo de las llamadas (nil sin ventana nativa).
	Video VideoHost
	// policy: configuración administrada por la organización.
	policy policyState
	// LogRing guarda el registro reciente para los informes de problemas (lo provee main).
	LogRing *LogRing
	// Snapshot captura la ventana (solo diagnóstico con HILVAN_SNAPSHOT).
	Snapshot func()

	ctx    context.Context
	cancel context.CancelFunc
}

func NewManager(paths Paths, version string, log zerolog.Logger) (*Manager, error) {
	for _, d := range []string{paths.Data, paths.Config, paths.Cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		Log:      log,
		Paths:    paths,
		Version:  version,
		settings: loadSettings(filepath.Join(paths.Config, "settings.json")),
		accounts: map[string]*Account{},
		subs:     map[int]chan Push{},
		pending:  map[string]*pendingLogin{},
		ctx:      ctx,
		cancel:   cancel,
	}
	useKeyring := m.settings.Get().UseKeyring && os.Getenv("HILVAN_NO_KEYRING") != "1"
	m.secrets = NewSecretStore(filepath.Join(paths.Data, "secrets.json"), useKeyring)
	m.Notifier = newNotifier(m)
	m.Calls = newCallManager(m)
	m.loadSystemPolicy()
	mautrix.DefaultUserAgent = userAgent(version)
	log.Info().Str("secrets", m.secrets.Kind()).Msg("Almacén de secretos listo")
	return m, nil
}

// Settings son los ajustes vigentes (con lo que fija la organización, si hay política).
func (m *Manager) Settings() Settings { return applyLocked(m.settings.Get(), m.Policy()) }

func (m *Manager) Context() context.Context { return m.ctx }

// Start restaura las sesiones guardadas.
func (m *Manager) Start() {
	m.syncAppIcon()
	go m.updateLoop()
	metas := m.loadMetas()
	for _, meta := range metas {
		meta := meta
		go func() {
			if _, err := m.restoreAccount(meta); err != nil {
				m.Log.Error().Err(err).Str("user", meta.UserID).Msg("No se pudo restaurar la cuenta")
				m.Broadcast(Push{Ev: "account.error", A: meta.UserID, D: map[string]string{"error": err.Error()}})
			}
		}()
	}
}

func (m *Manager) Stop() {
	m.cancel()
	m.mu.RLock()
	accts := make([]*Account, 0, len(m.accounts))
	for _, a := range m.accounts {
		accts = append(accts, a)
	}
	m.mu.RUnlock()
	var wg sync.WaitGroup
	for _, a := range accts {
		wg.Add(1)
		go func(a *Account) { defer wg.Done(); a.Close() }(a)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// --- suscriptores (interfaz) ---

func (m *Manager) Subscribe() (int, <-chan Push) {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	m.nextSub++
	ch := make(chan Push, 1024)
	m.subs[m.nextSub] = ch
	return m.nextSub, ch
}

func (m *Manager) Unsubscribe(id int) {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	if ch, ok := m.subs[id]; ok {
		close(ch)
		delete(m.subs, id)
	}
}

// OnPush registra una función que ve cada evento (por ejemplo la bandeja, para el punto de
// no leídos). Tiene que volver enseguida.
func (m *Manager) OnPush(f func(Push)) { m.pushHook.Store(&f) }

func (m *Manager) Broadcast(p Push) {
	if f := m.pushHook.Load(); f != nil && *f != nil {
		(*f)(p)
	}
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	for _, ch := range m.subs {
		select {
		case ch <- p:
		default:
			// Cliente lento: preferimos descartar antes que bloquear el sync.
			m.Log.Warn().Str("ev", p.Ev).Msg("Cola de la interfaz llena; evento descartado")
		}
	}
}

func (m *Manager) HasSubscribers() bool {
	m.subsMu.Lock()
	defer m.subsMu.Unlock()
	return len(m.subs) > 0
}

// --- foco (para no notificar lo que el usuario ya está viendo) ---

func (m *Manager) SetFocus(f Focus) {
	m.focusMu.Lock()
	m.focus = f
	m.focusMu.Unlock()
}

func (m *Manager) Focus() Focus {
	m.focusMu.RLock()
	defer m.focusMu.RUnlock()
	return m.focus
}

// --- cuentas ---

func (m *Manager) metasPath() string { return filepath.Join(m.Paths.Data, "accounts.json") }

func (m *Manager) loadMetas() []AccountMeta {
	var metas []AccountMeta
	b, err := os.ReadFile(m.metasPath())
	if err == nil {
		_ = json.Unmarshal(b, &metas)
	}
	sort.SliceStable(metas, func(i, j int) bool { return metas[i].Order < metas[j].Order })
	return metas
}

func (m *Manager) saveMetas() error {
	m.mu.RLock()
	metas := make([]AccountMeta, 0, len(m.accounts))
	for _, a := range m.accounts {
		metas = append(metas, a.Meta())
	}
	m.mu.RUnlock()
	sort.SliceStable(metas, func(i, j int) bool { return metas[i].Order < metas[j].Order })
	b, _ := json.MarshalIndent(metas, "", "  ")
	tmp := m.metasPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.metasPath())
}

func (m *Manager) Account(userID string) (*Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.accounts[userID]
	if !ok {
		return nil, fmt.Errorf("cuenta desconocida: %s", userID)
	}
	return a, nil
}

func (m *Manager) Accounts() []*Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Account, 0, len(m.accounts))
	for _, a := range m.accounts {
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].meta.Order < out[j].meta.Order })
	return out
}

func (m *Manager) accountDir(userID string) string {
	return filepath.Join(m.Paths.Data, "accounts", safeName(userID))
}

func (m *Manager) secretKey(userID string) string { return "account:" + userID }

func (m *Manager) loadSecrets(userID string) (*accountSecrets, error) {
	raw, err := m.secrets.Get(m.secretKey(userID))
	if err != nil {
		return nil, err
	}
	var s accountSecrets
	if err = json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (m *Manager) saveSecrets(userID string, s *accountSecrets) error {
	b, _ := json.Marshal(s)
	return m.secrets.Set(m.secretKey(userID), string(b))
}

func (m *Manager) restoreAccount(meta AccountMeta) (*Account, error) {
	sec, err := m.loadSecrets(meta.UserID)
	if err != nil {
		return nil, fmt.Errorf("no hay credenciales guardadas: %w", err)
	}
	a, err := newAccount(m, meta, sec)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.accounts[meta.UserID] = a
	m.mu.Unlock()
	m.Broadcast(Push{Ev: "accounts", D: m.AccountsInfo()})
	go a.Run()
	return a, nil
}

// addAccount registra una cuenta recién iniciada.
func (m *Manager) addAccount(meta AccountMeta, sec *accountSecrets) (*Account, error) {
	m.mu.RLock()
	_, exists := m.accounts[meta.UserID]
	n := len(m.accounts)
	m.mu.RUnlock()
	if exists {
		return nil, errors.New("esa cuenta ya está abierta en Hilvan")
	}
	if p := m.Policy(); p.MaxAccounts > 0 && n >= p.MaxAccounts || p.Disabled("multi_account") && n >= 1 {
		return nil, errors.New("tu organización no permite abrir más cuentas en Hilvan")
	}
	meta.AddedTS = time.Now().UnixMilli()
	meta.Order = n
	if err := m.saveSecrets(meta.UserID, sec); err != nil {
		return nil, fmt.Errorf("no se pudieron guardar las credenciales: %w", err)
	}
	a, err := newAccount(m, meta, sec)
	if err != nil {
		_ = m.secrets.Delete(m.secretKey(meta.UserID))
		return nil, err
	}
	m.mu.Lock()
	m.accounts[meta.UserID] = a
	m.mu.Unlock()
	if err = m.saveMetas(); err != nil {
		m.Log.Error().Err(err).Msg("No se pudo guardar accounts.json")
	}
	m.Broadcast(Push{Ev: "accounts", D: m.AccountsInfo()})
	go a.Run()
	return a, nil
}

func (m *Manager) removeAccount(userID string) {
	m.mu.Lock()
	a := m.accounts[userID]
	delete(m.accounts, userID)
	m.mu.Unlock()
	if a != nil {
		a.Close()
	}
	_ = m.secrets.Delete(m.secretKey(userID))
	_ = os.RemoveAll(m.accountDir(userID))
	_ = m.saveMetas()
	m.Broadcast(Push{Ev: "accounts", D: m.AccountsInfo()})
}

type AccountInfo struct {
	UserID      string `json:"user_id"`
	DeviceID    string `json:"device_id"`
	Homeserver  string `json:"homeserver"`
	DisplayName string `json:"displayname"`
	Avatar      string `json:"avatar"`
	Status      string `json:"status"`
	SyncMode    string `json:"sync_mode"`
	LoginType   string `json:"login_type"`
	Unread      int    `json:"unread"`
	Highlight   int    `json:"highlight"`
	Presence    string `json:"presence"`
	StatusMsg   string `json:"status_msg,omitempty"`
	StatusUntil int64  `json:"status_until,omitempty"`
}

func (m *Manager) AccountsInfo() []AccountInfo {
	var out []AccountInfo
	for _, a := range m.Accounts() {
		out = append(out, a.Info())
	}
	if out == nil {
		out = []AccountInfo{}
	}
	return out
}

func (m *Manager) UpdateSettings(patch json.RawMessage) (Settings, error) {
	var probe struct {
		Proxy     *string `json:"proxy"`
		Autostart *bool   `json:"autostart"`
	}
	_ = json.Unmarshal(patch, &probe)
	if probe.Proxy != nil {
		if err := validProxy(*probe.Proxy); err != nil {
			return m.Settings(), err
		}
	}
	if probe.Autostart != nil {
		if err := SetAutostart(*probe.Autostart); err != nil {
			return m.Settings(), fmt.Errorf("no se pudo configurar el inicio automático: %w", err)
		}
	}
	old := m.Settings()
	if _, err := m.settings.Patch(stripLocked(patch, m.Policy())); err != nil {
		return m.Settings(), err
	}
	s := m.Settings()
	m.Calls.SettingsChanged(old, s)
	if m.SetBackground != nil {
		m.SetBackground(s.CloseToTray)
	}
	if m.TrayChanged != nil && old.TrayIcon != s.TrayIcon {
		m.TrayChanged(s.TrayIcon)
	}
	m.Broadcast(Push{Ev: "settings", D: s})
	for _, a := range m.Accounts() {
		a.applySettings(s)
	}
	if m.OnSettings != nil {
		m.OnSettings(s)
	}
	return s, nil
}
