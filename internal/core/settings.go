package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// Paths agrupa los directorios de la app (XDG en Linux).
type Paths struct {
	Data   string // cuentas, bases de datos
	Config string // ajustes
	Cache  string // multimedia
	// Portable: perfil en una carpeta elegida (--data). No toca el lanzador del sistema.
	Portable bool
}

func DefaultPaths(override string) Paths {
	if override != "" {
		return Paths{Data: filepath.Join(override, "data"), Config: filepath.Join(override, "config"), Cache: filepath.Join(override, "cache"), Portable: true}
	}
	home, _ := os.UserHomeDir()
	cfg, err := os.UserConfigDir()
	if err != nil {
		cfg = filepath.Join(home, ".config")
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = filepath.Join(home, ".cache")
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		switch runtime.GOOS {
		case "windows":
			data = os.Getenv("LOCALAPPDATA")
		case "darwin":
			data = filepath.Join(home, "Library", "Application Support")
		default:
			data = filepath.Join(home, ".local", "share")
		}
	}
	return Paths{Data: filepath.Join(data, "hilvan"), Config: filepath.Join(cfg, "hilvan"), Cache: filepath.Join(cache, "hilvan")}
}

// Settings son las preferencias globales. Lo que solo le importa a la interfaz
// va en UI (mapa libre) para no tener que tocar Go por cada opción visual.
type Settings struct {
	Language             string `json:"language"`
	Theme                string `json:"theme"` // auto | light | dark
	Accent               string `json:"accent"`
	SendReadReceipts     bool   `json:"send_read_receipts"`
	PrivateReceipts      bool   `json:"private_receipts"`
	SendTyping           bool   `json:"send_typing"`
	ShowPresence         bool   `json:"show_presence"`
	Notifications        bool   `json:"notifications"`
	NotifyShowContent    bool   `json:"notify_show_content"`
	NotifySound          bool   `json:"notify_sound"`
	InvisibleCrypto      bool   `json:"invisible_crypto"` // MSC4153: solo dispositivos verificados por su dueño
	URLPreviews          bool   `json:"url_previews"`
	MediaAutoload        bool   `json:"media_autoload"`
	SlidingSync          string `json:"sliding_sync"` // auto | on | off
	UseKeyring           bool   `json:"use_keyring"`
	JitsiDomain          string `json:"jitsi_domain"`
	CallLowLatency       bool   `json:"call_low_latency"`
	CallNoiseSuppress    bool   `json:"call_noise_suppression"`
	CallEchoCancel       bool   `json:"call_echo_cancel"`
	CallCamera           string `json:"call_camera"`
	CallMic              string `json:"call_mic"`
	CallSpeaker          string `json:"call_speaker"`
	CallHWAccel          bool   `json:"call_hw_accel"`
	SpellCheck           bool   `json:"spellcheck"`
	SpellLangs           string `json:"spell_langs"`
	ShareHistoryOnInvite bool   `json:"share_history_on_invite"` // MSC4268

	// Red
	Proxy string `json:"proxy"` // "" | socks5://host:puerto | http://host:puerto

	// Comportamiento de la app
	TrayIcon    bool `json:"tray_icon"`     // ícono en la bandeja del sistema
	CloseToTray bool `json:"close_to_tray"` // al cerrar la ventana queda en la bandeja (sigue sincronizando)
	Autostart   bool `json:"autostart"`
	StartHidden bool `json:"start_hidden"`
	// Ícono de la app: "" es el clásico; si no, el estilo elegido (la imagen se guarda aparte).
	AppIcon    string          `json:"app_icon"`
	AppIconCfg json.RawMessage `json:"app_icon_cfg,omitempty"`
	AppIconRev string          `json:"app_icon_rev,omitempty"`
	// Buscar versiones nuevas de Hilvan (cada 12 horas).
	UpdateCheck bool `json:"update_check"`
	// Última carpeta elegida en "Guardar como…".
	LastSaveDir string `json:"last_save_dir,omitempty"` // cambia con cada ícono (para no mostrar uno viejo)

	// Notificaciones
	DNDEnabled  bool   `json:"dnd_enabled"`
	DNDFrom     string `json:"dnd_from"` // "22:00"
	DNDTo       string `json:"dnd_to"`   // "08:00"
	NotifyCalls bool   `json:"notify_calls"`

	// Chat
	URLPreviewsEncrypted bool `json:"url_previews_encrypted"`
	CompressImages       bool `json:"compress_images"`
	MediaCacheMB         int  `json:"media_cache_mb"`
	CallForceRelay       bool `json:"call_force_relay"` // privacidad: no exponer la IP en llamadas

	UI map[string]json.RawMessage `json:"ui,omitempty"`
}

func defaultSettings() Settings {
	return Settings{
		Language:             "",
		Theme:                "auto",
		Accent:               "#6d5dfc",
		SendReadReceipts:     true,
		SendTyping:           true,
		ShowPresence:         true,
		Notifications:        true,
		NotifyShowContent:    true,
		NotifySound:          true,
		InvisibleCrypto:      true,
		URLPreviews:          true,
		MediaAutoload:        true,
		SlidingSync:          "auto",
		UseKeyring:           true,
		JitsiDomain:          "meet.element.io",
		CallLowLatency:       true,
		CallNoiseSuppress:    true,
		CallEchoCancel:       true,
		CallHWAccel:          true,
		SpellCheck:           true,
		ShareHistoryOnInvite: true,
		DNDFrom:              "22:00",
		DNDTo:                "08:00",
		NotifyCalls:          true,
		CompressImages:       true,
		MediaCacheMB:         2048,
		TrayIcon:             true,
		UpdateCheck:          true,
		CloseToTray:          true,
	}
}

type settingsFile struct {
	path string
	mu   sync.RWMutex
	s    Settings
}

func loadSettings(path string) *settingsFile {
	sf := &settingsFile{path: path, s: defaultSettings()}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &sf.s)
	}
	if sf.s.Accent == "#3d7cf5" { // acento por defecto anterior al logo nuevo
		sf.s.Accent = "#6d5dfc"
	}
	return sf
}

func (sf *settingsFile) Get() Settings {
	sf.mu.RLock()
	defer sf.mu.RUnlock()
	return sf.s
}

// Patch aplica un JSON parcial sobre los ajustes y los guarda.
func (sf *settingsFile) Patch(patch json.RawMessage) (Settings, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	next := sf.s
	// Copia del mapa UI para fusionar claves en vez de reemplazarlo entero.
	ui := map[string]json.RawMessage{}
	for k, v := range sf.s.UI {
		ui[k] = v
	}
	var probe struct {
		UI map[string]json.RawMessage `json:"ui"`
	}
	_ = json.Unmarshal(patch, &probe)
	if err := json.Unmarshal(patch, &next); err != nil {
		return sf.s, err
	}
	for k, v := range probe.UI {
		if string(v) == "null" {
			delete(ui, k)
		} else {
			ui[k] = v
		}
	}
	next.UI = ui
	sf.s = next
	return sf.s, sf.saveLocked()
}

func (sf *settingsFile) saveLocked() error {
	b, err := json.MarshalIndent(sf.s, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(sf.path), 0o700); err != nil {
		return err
	}
	tmp := sf.path + ".tmp"
	if err = os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, sf.path)
}
