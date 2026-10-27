package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"golang.org/x/image/draw"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/pushrules"
)

// --- red: proxy (HTTP o SOCKS5, p. ej. Tor en socks5://127.0.0.1:9050) ---

func newHTTPClient(proxy string, verify ...func(tls.ConnectionState) error) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if len(verify) > 0 && verify[0] != nil {
		// Verificación TLS normal + fijación de certificados (VerifyConnection corre después).
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, VerifyConnection: verify[0]}
	}
	tr.MaxIdleConnsPerHost = 8
	tr.ResponseHeaderTimeout = 0
	if proxy = strings.TrimSpace(proxy); proxy != "" {
		if u, err := url.Parse(proxy); err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "socks5" || u.Scheme == "socks5h") {
			if u.Scheme == "socks5h" {
				u.Scheme = "socks5" // el dialer SOCKS5 de Go ya resuelve los nombres en el proxy
			}
			tr.Proxy = http.ProxyURL(u)
		}
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: tr}
}

func validProxy(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	u, err := url.Parse(p)
	if err != nil || u.Host == "" {
		return errors.New("dirección de proxy inválida (ejemplo: socks5://127.0.0.1:9050)")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return nil
	}
	return errors.New("tipo de proxy no soportado: usá http://, https:// o socks5://")
}

// --- no molestar ---

func inQuietHours(s Settings, now time.Time) bool {
	if !s.DNDEnabled {
		return false
	}
	parse := func(v string) int {
		var h, m int
		if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil {
			return -1
		}
		return h*60 + m
	}
	from, to := parse(s.DNDFrom), parse(s.DNDTo)
	if from < 0 || to < 0 {
		return true // sin horario válido: siempre activo
	}
	cur := now.Hour()*60 + now.Minute()
	if from == to {
		return true
	}
	if from < to {
		return cur >= from && cur < to
	}
	return cur >= from || cur < to
}

// --- inicio automático (XDG autostart) ---

func autostartPath() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		cfg = filepath.Join(home, ".config")
	}
	return filepath.Join(cfg, "autostart", "io.github.heelxel.Hilvan.desktop")
}

func SetAutostart(on bool) error {
	switch runtime.GOOS {
	case "windows":
		return setAutostartWindows(on)
	case "darwin":
		return setAutostartMac(on)
	case "android":
		return nil // lo maneja la app (al prender el teléfono)
	}
	p := autostartPath()
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe := "hilvan"
	if _, err := exec.LookPath("hilvan"); err != nil {
		if self, err := os.Executable(); err == nil {
			exe = self
		}
	}
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Hilvan
Comment=Mensajería Matrix
Exec=%s --background
Icon=io.github.heelxel.Hilvan
X-GNOME-Autostart-enabled=true
NoDisplay=false
`, exe)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// Windows: la clave Run del usuario (sin permisos de administrador).
func setAutostartWindows(on bool) error {
	const key = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	if !on {
		_ = exec.Command("reg", "delete", key, "/v", "Hilvan", "/f").Run()
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return exec.Command("reg", "add", key, "/v", "Hilvan", "/t", "REG_SZ", "/d", `"`+exe+`" --background`, "/f").Run()
}

// macOS: un LaunchAgent del usuario.
func setAutostartMac(on bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	p := filepath.Join(home, "Library", "LaunchAgents", "io.github.heelxel.hilvan.plist")
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(exe)
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>io.github.heelxel.hilvan</string>
  <key>ProgramArguments</key><array><string>` + esc + `</string><string>--background</string></array>
  <key>RunAtLoad</key><true/>
</dict></plist>
`
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(plist), 0o644)
}

// --- reglas de notificación del servidor (categorías y palabras clave) ---

type notifyCategory struct {
	Key   string
	Kind  pushrules.PushRuleType
	Rules []string
}

var notifyCategories = []notifyCategory{
	{"dm", pushrules.UnderrideRule, []string{".m.rule.room_one_to_one", ".m.rule.encrypted_room_one_to_one"}},
	{"group", pushrules.UnderrideRule, []string{".m.rule.message", ".m.rule.encrypted"}},
	{"mentions", pushrules.OverrideRule, []string{".m.rule.is_user_mention", ".m.rule.contains_display_name", ".m.rule.contains_user_name"}},
	{"room_mentions", pushrules.OverrideRule, []string{".m.rule.is_room_mention", ".m.rule.roomnotif"}},
	{"invites", pushrules.OverrideRule, []string{".m.rule.invite_for_me"}},
	{"calls", pushrules.UnderrideRule, []string{".m.rule.call"}},
	{"bots", pushrules.OverrideRule, []string{".m.rule.suppress_notices"}},
}

func findRule(rs *pushrules.PushRuleset, kind pushrules.PushRuleType, ruleID string) *pushrules.PushRule {
	if rs == nil {
		return nil
	}
	var arr pushrules.PushRuleArray
	switch kind {
	case pushrules.OverrideRule:
		arr = rs.Override
	case pushrules.UnderrideRule:
		arr = rs.Underride
	case pushrules.ContentRule:
		arr = rs.Content
	}
	for _, r := range arr {
		if r.RuleID == ruleID {
			return r
		}
	}
	return nil
}

func (a *Account) NotifyRules(ctx context.Context) (map[string]any, error) {
	rs, err := a.cli.GetPushRules(ctx)
	if err != nil {
		return nil, friendlyError(err)
	}
	a.pushRules.Store(rs)
	cats := map[string]bool{}
	for _, c := range notifyCategories {
		on := false
		found := false
		for _, id := range c.Rules {
			if r := findRule(rs, c.Kind, id); r != nil {
				found = true
				if r.Enabled && (c.Key == "bots" || actionsNotify(r.Actions)) {
					on = true
				}
			}
		}
		if found {
			cats[c.Key] = on
		}
	}
	var keywords []string
	for _, r := range rs.Content {
		if !strings.HasPrefix(r.RuleID, ".") && r.Pattern != "" {
			keywords = append(keywords, r.Pattern)
		}
	}
	return map[string]any{"categories": cats, "keywords": keywords}, nil
}

func (a *Account) SetNotifyCategory(ctx context.Context, key string, on bool) error {
	for _, c := range notifyCategories {
		if c.Key != key {
			continue
		}
		var firstErr error
		for _, id := range c.Rules {
			if err := a.cli.SetPushRuleEnabled(ctx, "global", c.Kind, id, on); err != nil && firstErr == nil {
				var httpErr mautrix.HTTPError
				if !(errors.As(err, &httpErr) && httpErr.IsStatus(404)) {
					firstErr = err
				}
			}
		}
		return friendlyErrorNil(firstErr)
	}
	return errors.New("categoría desconocida")
}

func (a *Account) AddKeyword(ctx context.Context, kw string) error {
	kw = strings.TrimSpace(kw)
	if kw == "" {
		return errors.New("palabra vacía")
	}
	return friendlyErrorNil(a.cli.PutPushRule(ctx, "global", pushrules.ContentRule, kw, &mautrix.ReqPutPushRule{
		Pattern: kw,
		Actions: []*pushrules.PushAction{
			{Action: pushrules.ActionNotify},
			{Action: pushrules.ActionSetTweak, Tweak: pushrules.TweakSound, Value: "default"},
			{Action: pushrules.ActionSetTweak, Tweak: pushrules.TweakHighlight},
		},
	}))
}

func (a *Account) RemoveKeyword(ctx context.Context, kw string) error {
	return friendlyErrorNil(a.cli.DeletePushRule(ctx, "global", pushrules.ContentRule, kw))
}

// --- gestión de cuenta (con autenticación interactiva) ---

func (a *Account) uiaRequest(ctx context.Context, method, u string, body map[string]any, password string) error {
	_, err := a.cli.MakeRequest(ctx, method, u, body, nil)
	var httpErr mautrix.HTTPError
	if errors.As(err, &httpErr) && httpErr.IsStatus(401) {
		var uia mautrix.RespUserInteractive
		_ = json.Unmarshal([]byte(httpErr.ResponseBody), &uia)
		if password == "" {
			return errors.New("password_required")
		}
		body["auth"] = map[string]any{
			"type": "m.login.password", "session": uia.Session, "password": password,
			"identifier": map[string]any{"type": "m.id.user", "user": a.UserID()},
		}
		_, err = a.cli.MakeRequest(ctx, method, u, body, nil)
	}
	return friendlyErrorNil(err)
}

func (a *Account) ChangePassword(ctx context.Context, oldPass, newPass string, logoutOthers bool) error {
	if a.Meta().LoginType == "oauth" {
		return &ApprovalRequired{URL: a.accountManagementURL("")}
	}
	if len(newPass) < 8 {
		return errors.New("la contraseña nueva debe tener al menos 8 caracteres")
	}
	u := a.cli.BuildClientURL("v3", "account", "password")
	return a.uiaRequest(ctx, http.MethodPost, u, map[string]any{"new_password": newPass, "logout_devices": logoutOthers}, oldPass)
}

func (a *Account) Deactivate(ctx context.Context, password string, erase bool) error {
	if a.Meta().LoginType == "oauth" {
		return &ApprovalRequired{URL: a.accountManagementURL("org.matrix.account_deactivate")}
	}
	u := a.cli.BuildClientURL("v3", "account", "deactivate")
	if err := a.uiaRequest(ctx, http.MethodPost, u, map[string]any{"erase": erase}, password); err != nil {
		return err
	}
	go a.m.removeAccount(a.UserID())
	return nil
}

func (a *Account) SignOutOthers(ctx context.Context, password string) error {
	resp, err := a.cli.GetDevicesInfo(ctx)
	if err != nil {
		return friendlyError(err)
	}
	var ids []string
	for _, d := range resp.Devices {
		if d.DeviceID != a.cli.DeviceID {
			ids = append(ids, d.DeviceID.String())
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return a.DeleteDevices(ctx, ids, password)
}

func (a *Account) ThreePIDs(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	_, err := a.cli.MakeRequest(ctx, http.MethodGet, a.cli.BuildClientURL("v3", "account", "3pid"), nil, &out)
	return out, friendlyErrorNil(err)
}

func (a *Account) DeleteThreePID(ctx context.Context, medium, address string) error {
	_, err := a.cli.MakeRequest(ctx, http.MethodPost, a.cli.BuildClientURL("v3", "account", "3pid", "delete"),
		map[string]any{"medium": medium, "address": address}, nil)
	return friendlyErrorNil(err)
}

var emailSecrets sync.Map // sid -> client_secret

func (a *Account) RequestEmailToken(ctx context.Context, email string) (string, error) {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	secret := hex.EncodeToString(b)
	var resp struct {
		SID string `json:"sid"`
	}
	_, err := a.cli.MakeRequest(ctx, http.MethodPost, a.cli.BuildClientURL("v3", "account", "3pid", "email", "requestToken"),
		map[string]any{"client_secret": secret, "email": email, "send_attempt": 1}, &resp)
	if err != nil {
		return "", friendlyError(err)
	}
	emailSecrets.Store(resp.SID, secret)
	return resp.SID, nil
}

func (a *Account) AddThreePID(ctx context.Context, sid, password string) error {
	v, ok := emailSecrets.Load(sid)
	if !ok {
		return errors.New("solicitud vencida; volvé a pedir el correo de confirmación")
	}
	err := a.uiaRequest(ctx, http.MethodPost, a.cli.BuildClientURL("v3", "account", "3pid", "add"),
		map[string]any{"client_secret": v.(string), "sid": sid}, password)
	if err == nil {
		emailSecrets.Delete(sid)
	}
	return err
}

func (a *Account) IgnoredUsers() []string {
	a.ignoredMu.RLock()
	defer a.ignoredMu.RUnlock()
	out := make([]string, 0, len(a.ignored))
	for u := range a.ignored {
		out = append(out, u)
	}
	return out
}

// Resync borra la caché de mensajes y vuelve a sincronizar desde cero (las claves se conservan).
func (a *Account) Resync(ctx context.Context) error {
	for _, r := range a.store.AllRooms() {
		a.store.ForgetRoom(r.ID)
	}
	if err := a.cli.Store.SaveNextBatch(ctx, a.cli.UserID, ""); err != nil {
		return err
	}
	a.store.SetKV("sss_pos", "")
	a.initialDone.Store(false)
	a.cli.StopSync()
	a.m.Broadcast(Push{Ev: "resync", A: a.UserID()})
	return nil
}

// --- vista previa de enlaces (por el servidor, con caché en memoria) ---

type previewEntry struct {
	data json.RawMessage
	at   time.Time
}

var previewCache sync.Map

func (a *Account) URLPreview(ctx context.Context, link string) (json.RawMessage, error) {
	if a.m.Policy().Disabled("url_previews") {
		return nil, errors.New("tu organización desactivó las vistas previas de enlaces")
	}
	if !strings.HasPrefix(link, "https://") && !strings.HasPrefix(link, "http://") {
		return nil, errors.New("enlace no soportado")
	}
	key := a.UserID() + "|" + link
	if v, ok := previewCache.Load(key); ok && time.Since(v.(previewEntry).at) < 12*time.Hour {
		return v.(previewEntry).data, nil
	}
	resp, err := a.cli.GetURLPreview(ctx, link)
	if err != nil {
		return nil, friendlyError(err)
	}
	b, _ := json.Marshal(resp)
	// Solo campos útiles para la tarjeta.
	p := gjson.ParseBytes(b)
	out, _ := json.Marshal(map[string]any{
		"title": p.Get("og:title").Str, "description": p.Get("og:description").Str,
		"image": p.Get("og:image").Str, "site": p.Get("og:site_name").Str,
		"w": p.Get("og:image:width").Int(), "h": p.Get("og:image:height").Int(),
	})
	previewCache.Store(key, previewEntry{data: out, at: time.Now()})
	return out, nil
}

// --- compresión de imágenes antes de subir ---

// compressImage reduce fotos grandes (máx. 2560 px, JPEG 85). Devuelve los datos originales si no conviene.
func compressImage(data []byte, mime string) ([]byte, string, bool) {
	if len(data) < 1500*1024 || (mime != "image/jpeg" && mime != "image/png" && mime != "image/webp") {
		return data, mime, false
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, mime, false
	}
	// PNG con transparencia: no lo convertimos a JPEG.
	if format == "png" {
		if o, ok := img.(interface{ Opaque() bool }); ok && !o.Opaque() {
			return data, mime, false
		}
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := min(2560/float64(max(w, h)), 1)
	tw, th := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	var dst image.Image = img
	if scale < 1 {
		rgba := image.NewRGBA(image.Rect(0, 0, tw, th))
		draw.CatmullRom.Scale(rgba, rgba.Bounds(), img, b, draw.Over, nil)
		dst = rgba
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return data, mime, false
	}
	if buf.Len() >= len(data) {
		return data, mime, false
	}
	return buf.Bytes(), "image/jpeg", true
}

var _ = id.UserID("")
