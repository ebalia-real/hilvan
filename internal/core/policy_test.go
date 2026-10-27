package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix"
)

func TestPolicyLockedSettings(t *testing.T) {
	dir := t.TempDir()
	pol := filepath.Join(dir, "policy.json")
	_ = os.WriteFile(pol, []byte(`{
		"organization": "Ejemplo SA",
		"allowed_homeservers": ["ejemplo.com"],
		"force_encryption": true,
		"disable": ["calls", "url_previews"],
		"brand": {"name": "Chat Ejemplo", "accent": "#123456"},
		"locked_settings": {"proxy": "socks5://127.0.0.1:9050", "ui.joinMuted": true}
	}`), 0o600)
	t.Setenv("HILVAN_POLICY", pol)
	t.Setenv("HILVAN_NO_KEYRING", "1")
	m, err := NewManager(DefaultPaths(filepath.Join(dir, "data")), "test", zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	p := m.Policy()
	if !p.ForceEncryption || !p.Disabled("calls") || p.Disabled("screenshare") {
		t.Fatalf("política mal leída: %+v", p)
	}
	if !p.AllowsHomeserver("https://matrix.ejemplo.com", "ejemplo.com") || p.AllowsHomeserver("https://matrix.org", "matrix.org") {
		t.Fatal("lista de servidores permitidos mal aplicada")
	}
	s := m.Settings()
	if s.Proxy != "socks5://127.0.0.1:9050" || s.Accent != "#123456" || string(s.UI["joinMuted"]) != "true" {
		t.Fatalf("ajustes bloqueados no aplicados: proxy=%q accent=%q", s.Proxy, s.Accent)
	}
	// El usuario no puede cambiar lo bloqueado, pero sí lo demás.
	if _, err := m.UpdateSettings(json.RawMessage(`{"proxy":"","accent":"#ff0000","notifications":false,"ui":{"joinMuted":false,"compact":true}}`)); err != nil {
		t.Fatal(err)
	}
	s = m.Settings()
	if s.Proxy != "socks5://127.0.0.1:9050" || s.Accent != "#123456" || string(s.UI["joinMuted"]) != "true" {
		t.Fatal("se pudo cambiar un ajuste bloqueado")
	}
	if s.Notifications || string(s.UI["compact"]) != "true" {
		t.Fatal("no se aplicó un ajuste libre")
	}
	if _, err := m.Discover(context.Background(), "matrix.org"); err == nil || !strings.Contains(err.Error(), "solo permite") {
		t.Fatalf("debió rechazar un servidor no permitido: %v", err)
	}
}

func TestCertPinning(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	good := spkiPin(srv.Certificate().RawSubjectPublicKeyInfo)
	try := func(pins []string) error {
		cli := newHTTPClient("", pinVerifier(func(string) []string { return pins }, nil))
		tr := cli.Transport.(*http.Transport)
		tr.TLSClientConfig.RootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		resp, err := cli.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := try([]string{good}); err != nil {
		t.Fatalf("con la clave correcta debió conectar: %v", err)
	}
	if err := try(nil); err != nil {
		t.Fatalf("sin claves fijadas debió conectar: %v", err)
	}
	if err := try([]string{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}); err == nil || !strings.Contains(err.Error(), "no coincide con el fijado") {
		t.Fatalf("con una clave distinta debió bloquear: %v", err)
	}
}

func TestContentScanner(t *testing.T) {
	scanner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "virus") {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"clean": false, "info": "***VIRUS DETECTED***"}`))
			return
		}
		_, _ = w.Write([]byte(`{"clean": true, "info": "File is clean"}`))
	}))
	defer scanner.Close()
	dir := t.TempDir()
	pol := filepath.Join(dir, "policy.json")
	_ = os.WriteFile(pol, []byte(`{"content_scanner": "`+scanner.URL+`"}`), 0o600)
	t.Setenv("HILVAN_POLICY", pol)
	t.Setenv("HILVAN_NO_KEYRING", "1")
	m, err := NewManager(DefaultPaths(filepath.Join(dir, "data")), "test", zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	a := &Account{m: m}
	a.cli, _ = newTestClient()
	if err := a.scanFile(context.Background(), fileRef{URL: "mxc://ejemplo.com/limpio"}); err != nil {
		t.Fatalf("archivo limpio bloqueado: %v", err)
	}
	if err := a.scanFile(context.Background(), fileRef{URL: "mxc://ejemplo.com/virus"}); err == nil || !strings.Contains(err.Error(), "antivirus") {
		t.Fatalf("archivo infectado no bloqueado: %v", err)
	}
}

func newTestClient() (*mautrix.Client, error) {
	cli, err := mautrix.NewClient("https://ejemplo.com", "@yo:ejemplo.com", "token")
	if err != nil {
		return nil, err
	}
	cli.Client = newHTTPClient("")
	return cli, nil
}

func TestVersionRanges(t *testing.T) {
	cases := []struct {
		v, rng string
		want   bool
	}{
		{"1.161.0", "< 1.162.0", true},
		{"1.161.0", "< 1.161.0", false},
		{"1.161.0", ">= 1.100.0, < 1.161.1", true},
		{"1.161.0", ">= 1.100.0, < 1.160.2", false},
		{"1.161.0", "<= 1.161.0", true},
	}
	for _, c := range cases {
		if got := versionInRange(c.v, c.rng); got != c.want {
			t.Errorf("%s en %q = %v", c.v, c.rng, got)
		}
	}
	if !versionLess("1.99.0", "1.161.0") || versionLess("1.161.0", "1.161.0") {
		t.Error("versionLess")
	}
	// En el binario real la lista trae todos los módulos (hilvan --sbom); acá alcanza con el formato.
	if b := SBOM("prueba"); !strings.Contains(string(b), `"bomFormat": "CycloneDX"`) || !strings.Contains(string(b), `"specVersion": "1.5"`) {
		t.Errorf("SBOM incompleto: %s", b[:min(300, len(b))])
	}
}
