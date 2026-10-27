package core

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
)

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{{"v0.2.0", "0.1.0", true}, {"0.1.1", "0.1.0-android", true}, {"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", false}, {"v1.0.0", "0.9.9", true}, {"basura", "0.1.0", false}}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// Un servidor de versiones falso: comprueba que solo se acepta lo firmado y lo que coincide.
func TestUpdateVerification(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	oldKey, oldAPI, oldPkg := updatePublicKey, updateAPI, Packaging
	updatePublicKey = base64.StdEncoding.EncodeToString(pub)
	Packaging = "windows"
	t.Cleanup(func() { updatePublicKey, updateAPI, Packaging = oldKey, oldAPI, oldPkg })

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	fw, _ := zw.Create("hilvan.exe")
	_, _ = fw.Write([]byte("binario nuevo"))
	_ = zw.Close()
	asset := assetName("9.9.9")
	sum := sha256.Sum256(zbuf.Bytes())
	sums := []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset))
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums))
	files := map[string][]byte{asset: zbuf.Bytes(), "SHA256SUMS": sums, "SHA256SUMS.sig": []byte(sig)}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			var assets []map[string]any
			for name, b := range files {
				assets = append(assets, map[string]any{"name": name, "browser_download_url": srv.URL + "/f/" + name, "size": len(b)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v9.9.9", "html_url": "x", "assets": assets})
			return
		}
		b, ok := files[filepath.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	updateAPI = srv.URL + "/latest"

	t.Setenv("HILVAN_NO_KEYRING", "1")
	m, err := NewManager(DefaultPaths(t.TempDir()), "0.1.0", zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	info, err := m.CheckUpdate(context.Background())
	if err != nil || info.State != "available" || info.Asset != asset {
		t.Fatalf("buscar: %+v %v", info, err)
	}

	rel := m.upd.release
	dir := t.TempDir()
	got, err := m.verifiedSums(context.Background(), rel, dir)
	if err != nil || got[asset] != hex.EncodeToString(sum[:]) {
		t.Fatalf("lista firmada: %v %v", got, err)
	}
	// Firma alterada: se rechaza.
	files["SHA256SUMS.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("otra cosa"))))
	if _, err := m.verifiedSums(context.Background(), rel, t.TempDir()); err == nil {
		t.Fatal("aceptó una firma que no corresponde")
	}
	files["SHA256SUMS.sig"] = []byte(sig)
	// Archivo alterado en el servidor: la descarga no coincide con la huella firmada.
	files[asset] = []byte("binario malicioso")
	m.upd.info.State = "available"
	if err := m.installUpdate(rel, m.upd.info); err == nil {
		t.Fatal("instaló un archivo alterado")
	}
	// Sin firma: no se instala.
	delete(files, "SHA256SUMS.sig")
	if _, err := m.verifiedSums(context.Background(), rel, t.TempDir()); err == nil {
		t.Fatal("aceptó una versión sin firma")
	}

	// extractOne saca el ejecutable del paquete.
	zp := filepath.Join(dir, "p.zip")
	_ = os.WriteFile(zp, zbuf.Bytes(), 0o600)
	out := filepath.Join(dir, "sale.exe")
	if err := extractOne(zp, "hilvan.exe", out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "binario nuevo" {
		t.Fatal("contenido extraído")
	}
}
