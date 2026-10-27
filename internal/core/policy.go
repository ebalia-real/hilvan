package core

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// Política administrada (como Element Pro): la organización controla Hilvan desde un archivo del
// sistema (MDM, despliegue) o desde su servidor (.well-known/matrix/client → "io.hilvan.policy").
// Lo que define la política no se puede cambiar desde los ajustes.

type Brand struct {
	Name    string `json:"name,omitempty"`
	Accent  string `json:"accent,omitempty"`
	LogoURL string `json:"logo_url,omitempty"`
}

type Policy struct {
	Sources             []string                   `json:"sources,omitempty"`
	Organization        string                     `json:"organization,omitempty"`
	DefaultHomeserver   string                     `json:"default_homeserver,omitempty"`
	AllowedHomeservers  []string                   `json:"allowed_homeservers,omitempty"`
	DisableRegistration bool                       `json:"disable_registration,omitempty"`
	ForceEncryption     bool                       `json:"force_encryption,omitempty"`
	DisableEncryption   bool                       `json:"disable_encryption,omitempty"` // io.element.e2ee force_disable
	Disable             []string                   `json:"disable,omitempty"`            // calls, screenshare, url_previews, public_rooms, bridges, jitsi, labs, export, file_upload, multi_account
	Brand               *Brand                     `json:"brand,omitempty"`
	Locked              map[string]json.RawMessage `json:"locked_settings,omitempty"`
	CertPins            map[string][]string        `json:"cert_pins,omitempty"` // host → SHA-256 (base64) de la clave pública (SPKI)
	ContentScanner      string                     `json:"content_scanner,omitempty"`
	ReportURL           string                     `json:"report_url,omitempty"`
	RetentionDays       int                        `json:"retention_default_days,omitempty"`
	MaxAccounts         int                        `json:"max_accounts,omitempty"`
	SupportURL          string                     `json:"support_url,omitempty"`
}

// Disabled indica si la organización apagó una función.
func (p *Policy) Disabled(feature string) bool {
	return p != nil && slices.Contains(p.Disable, feature)
}

// AllowsHomeserver comprueba la lista de servidores permitidos (vacía = cualquiera).
func (p *Policy) AllowsHomeserver(hs, serverName string) bool {
	if p == nil || len(p.AllowedHomeservers) == 0 {
		return true
	}
	for _, a := range p.AllowedHomeservers {
		a = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(a)), "/")
		if a == strings.ToLower(serverName) || a == strings.TrimSuffix(strings.ToLower(hs), "/") {
			return true
		}
	}
	return false
}

// AllowsInput revisa lo que escribió el usuario antes de contactar a nadie (un servidor no
// permitido ni siquiera recibe la consulta). Acepta subdominios de los permitidos.
func (p *Policy) AllowsInput(input string) bool {
	if p == nil || len(p.AllowedHomeservers) == 0 {
		return true
	}
	host := strings.ToLower(strings.TrimSpace(input))
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	for _, a := range p.AllowedHomeservers {
		a = strings.ToLower(strings.TrimSpace(a))
		a = strings.TrimPrefix(strings.TrimPrefix(a, "https://"), "http://")
		a = strings.TrimSuffix(a, "/")
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

func systemPolicyPaths() []string {
	var out []string
	if p := os.Getenv("HILVAN_POLICY"); p != "" {
		out = append(out, p)
	}
	switch runtime.GOOS {
	case "windows":
		out = append(out, filepath.Join(os.Getenv("ProgramData"), "Hilvan", "policy.json"))
	case "darwin":
		out = append(out, "/Library/Application Support/Hilvan/policy.json")
	default:
		out = append(out, "/etc/hilvan/policy.json")
	}
	return out
}

// mergePolicy combina dos políticas; la primera (sistema) gana en valores sueltos, las listas se unen.
func mergePolicy(a, b *Policy) *Policy {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := *a
	out.Sources = append(append([]string{}, a.Sources...), b.Sources...)
	pick := func(x, y string) string {
		if x != "" {
			return x
		}
		return y
	}
	out.Organization = pick(a.Organization, b.Organization)
	out.DefaultHomeserver = pick(a.DefaultHomeserver, b.DefaultHomeserver)
	out.ContentScanner = pick(a.ContentScanner, b.ContentScanner)
	out.ReportURL = pick(a.ReportURL, b.ReportURL)
	out.SupportURL = pick(a.SupportURL, b.SupportURL)
	if len(a.AllowedHomeservers) == 0 {
		out.AllowedHomeservers = b.AllowedHomeservers
	}
	out.DisableRegistration = a.DisableRegistration || b.DisableRegistration
	out.ForceEncryption = a.ForceEncryption || b.ForceEncryption
	out.DisableEncryption = (a.DisableEncryption || b.DisableEncryption) && !out.ForceEncryption
	for _, d := range b.Disable {
		if !slices.Contains(out.Disable, d) {
			out.Disable = append(out.Disable, d)
		}
	}
	if out.Brand == nil {
		out.Brand = b.Brand
	}
	out.Locked = map[string]json.RawMessage{}
	for k, v := range b.Locked {
		out.Locked[k] = v
	}
	for k, v := range a.Locked {
		out.Locked[k] = v
	}
	out.CertPins = map[string][]string{}
	for _, src := range []map[string][]string{b.CertPins, a.CertPins} {
		for h, pins := range src {
			out.CertPins[strings.ToLower(h)] = append(out.CertPins[strings.ToLower(h)], pins...)
		}
	}
	if out.RetentionDays == 0 {
		out.RetentionDays = b.RetentionDays
	}
	if out.MaxAccounts == 0 || (b.MaxAccounts > 0 && b.MaxAccounts < out.MaxAccounts) {
		if b.MaxAccounts > 0 {
			out.MaxAccounts = b.MaxAccounts
		}
	}
	return &out
}

type policyState struct {
	mu     sync.RWMutex
	system *Policy
	server map[string]*Policy // servidor → política publicada por ese servidor
}

func (m *Manager) loadSystemPolicy() {
	var merged *Policy
	for _, p := range systemPolicyPaths() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var pol Policy
		if err := json.Unmarshal(b, &pol); err != nil {
			m.Log.Warn().Err(err).Str("path", p).Msg("Política del sistema inválida")
			continue
		}
		pol.Sources = []string{p}
		merged = mergePolicy(merged, &pol)
	}
	m.policy.mu.Lock()
	m.policy.system = merged
	m.policy.mu.Unlock()
}

// Policy devuelve la política vigente (sistema + servidores de las cuentas abiertas).
func (m *Manager) Policy() *Policy {
	m.policy.mu.RLock()
	defer m.policy.mu.RUnlock()
	out := m.policy.system
	for _, p := range m.policy.server {
		out = mergePolicy(out, p)
	}
	if out == nil {
		return &Policy{}
	}
	return out
}

// fetchServerPolicy lee la política publicada por el servidor de una cuenta (.well-known).
func (a *Account) fetchServerPolicy(ctx context.Context) {
	server := a.cli.UserID.Homeserver()
	if server == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body []byte
	for _, base := range []string{"https://" + server, strings.TrimSuffix(a.cli.HomeserverURL.String(), "/")} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/.well-known/matrix/client", nil)
		resp, err := a.cli.Client.Do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		resp.Body.Close()
		if resp.StatusCode == 200 && gjson.ValidBytes(b) {
			body = b
			break
		}
	}
	var pol *Policy
	if body != nil {
		if raw := gjson.GetBytes(body, `io\.hilvan\.policy`); raw.IsObject() {
			var p Policy
			if json.Unmarshal([]byte(raw.Raw), &p) == nil {
				pol = &p
			}
		}
		// Compatibilidad con la configuración de cifrado de Element.
		if e2ee := gjson.GetBytes(body, `io\.element\.e2ee`); e2ee.Exists() {
			if pol == nil {
				pol = &Policy{}
			}
			if e2ee.Get("force_disable").Bool() {
				pol.DisableEncryption = true
			}
		}
	}
	a.m.policy.mu.Lock()
	if a.m.policy.server == nil {
		a.m.policy.server = map[string]*Policy{}
	}
	if pol != nil {
		pol.Sources = []string{server + "/.well-known/matrix/client"}
		a.m.policy.server[server] = pol
	} else {
		delete(a.m.policy.server, server)
	}
	a.m.policy.mu.Unlock()
	a.m.Broadcast(Push{Ev: "policy", D: a.m.Policy()})
}

// applyLocked impone los ajustes que fija la organización.
func applyLocked(s Settings, p *Policy) Settings {
	if p == nil || len(p.Locked) == 0 {
		return s
	}
	raw, _ := json.Marshal(s)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	ui := map[string]json.RawMessage{}
	for k, v := range s.UI {
		ui[k] = v
	}
	for k, v := range p.Locked {
		if strings.HasPrefix(k, "ui.") {
			ui[strings.TrimPrefix(k, "ui.")] = v
		} else {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	var out Settings
	if json.Unmarshal(b, &out) != nil {
		return s
	}
	out.UI = ui
	if p.Brand != nil && p.Brand.Accent != "" {
		out.Accent = p.Brand.Accent
	}
	return out
}

// stripLocked quita de un cambio de ajustes lo que la organización bloqueó.
func stripLocked(patch json.RawMessage, p *Policy) json.RawMessage {
	if p == nil || (len(p.Locked) == 0 && (p.Brand == nil || p.Brand.Accent == "")) {
		return patch
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(patch, &m) != nil {
		return patch
	}
	var ui map[string]json.RawMessage
	if raw, ok := m["ui"]; ok {
		_ = json.Unmarshal(raw, &ui)
	}
	for k := range p.Locked {
		if strings.HasPrefix(k, "ui.") {
			delete(ui, strings.TrimPrefix(k, "ui."))
		} else {
			delete(m, k)
		}
	}
	if p.Brand != nil && p.Brand.Accent != "" {
		delete(m, "accent")
	}
	if ui != nil {
		m["ui"], _ = json.Marshal(ui)
	}
	out, _ := json.Marshal(m)
	return out
}

// --- fijación de certificados (certificate pinning) ---

// spkiPin es el SHA-256 (base64) de la clave pública de un certificado.
func spkiPin(raw []byte) string {
	h := sha256.Sum256(raw)
	return base64.StdEncoding.EncodeToString(h[:])
}

// pinVerifier comprueba que alguna clave de la cadena coincida con las fijadas para el host.
// La verificación TLS normal se hace igual; esto es una exigencia extra.
func pinVerifier(pins func(host string) []string, onSeen func(host string, chain []string)) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		host := strings.ToLower(cs.ServerName)
		var chain []string
		for _, c := range cs.PeerCertificates {
			chain = append(chain, spkiPin(c.RawSubjectPublicKeyInfo))
		}
		if onSeen != nil {
			onSeen(host, chain)
		}
		want := pins(host)
		if len(want) == 0 {
			return nil
		}
		for _, have := range chain {
			for _, w := range want {
				if strings.TrimRight(w, "=") == strings.TrimRight(have, "=") {
					return nil
				}
			}
		}
		return fmt.Errorf("el certificado de %s no coincide con el fijado: la conexión se bloqueó para protegerte (posible interceptación)", host)
	}
}

// --- antivirus de la organización (matrix-content-scanner) ---

var errInfected = errors.New("el antivirus de tu organización bloqueó este archivo")

type scanResult struct {
	clean bool
	info  string
	at    time.Time
}

var scanCache sync.Map // url del archivo → scanResult

// scanFile consulta al escáner de contenido antes de entregar un archivo.
func (a *Account) scanFile(ctx context.Context, ref fileRef) error {
	scanner := strings.TrimSuffix(a.m.Policy().ContentScanner, "/")
	if scanner == "" {
		return nil
	}
	key := ref.URL
	if ref.File != nil {
		key = string(ref.File.URL)
	}
	if v, ok := scanCache.Load(key); ok && time.Since(v.(scanResult).at) < 6*time.Hour {
		if !v.(scanResult).clean {
			return fmt.Errorf("%w: %s", errInfected, v.(scanResult).info)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var req *http.Request
	if ref.File != nil {
		body, _ := json.Marshal(map[string]any{"file": ref.File})
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, scanner+"/_matrix/media_proxy/unstable/scan_encrypted", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
	} else {
		mxc := strings.TrimPrefix(ref.URL, "mxc://")
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, scanner+"/_matrix/media_proxy/unstable/scan/"+mxc, nil)
	}
	req.Header.Set("Authorization", "Bearer "+a.cli.AccessToken)
	resp, err := a.cli.Client.Do(req)
	if err != nil {
		return fmt.Errorf("no se pudo consultar el antivirus de tu organización: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	clean := gjson.GetBytes(b, "clean")
	if !clean.Exists() {
		return fmt.Errorf("respuesta inesperada del antivirus (HTTP %d)", resp.StatusCode)
	}
	res := scanResult{clean: clean.Bool(), info: gjson.GetBytes(b, "info").Str, at: time.Now()}
	scanCache.Store(key, res)
	if !res.clean {
		return fmt.Errorf("%w: %s", errInfected, res.info)
	}
	return nil
}

// --- política por cuenta: lectura periódica y fijación de certificados TOFU ---

func (a *Account) policyLoop() {
	a.fetchServerPolicy(a.ctx)
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
			a.fetchServerPolicy(a.ctx)
		}
	}
}

// PinningEnabled: el usuario pidió fijar el certificado de su servidor ("confiar en el primer uso").
func (a *Account) PinningEnabled() bool { return a.store.GetKV("cert_pinning") == "1" }

func (a *Account) storedPins() map[string][]string {
	var m map[string][]string
	_ = json.Unmarshal([]byte(a.store.GetKV("cert_pins")), &m)
	return m
}

func (a *Account) certPins(host string) []string {
	pins := append([]string{}, a.m.Policy().CertPins[host]...)
	if a.PinningEnabled() {
		pins = append(pins, a.storedPins()[host]...)
	}
	return pins
}

// onSeenCert guarda, la primera vez, las claves de la cadena del servidor.
func (a *Account) onSeenCert(host string, chain []string) {
	if !a.PinningEnabled() || len(chain) == 0 {
		return
	}
	m := a.storedPins()
	if len(m[host]) > 0 {
		return
	}
	if m == nil {
		m = map[string][]string{}
	}
	m[host] = chain
	b, _ := json.Marshal(m)
	a.store.SetKV("cert_pins", string(b))
}

// SetPinning activa o desactiva la fijación (y olvida las claves guardadas para volver a aprenderlas).
func (a *Account) SetPinning(on bool) {
	v := "0"
	if on {
		v = "1"
	}
	a.store.SetKV("cert_pinning", v)
	a.store.SetKV("cert_pins", "{}")
	a.cli.Client.CloseIdleConnections()
}

// PinInfo describe las claves fijadas (para mostrar en Seguridad).
func (a *Account) PinInfo() map[string]any {
	return map[string]any{"enabled": a.PinningEnabled(), "pins": a.storedPins(), "policy": a.m.Policy().CertPins}
}
