//go:build windows || darwin

package main

// Ventana de Hilvan en Windows y macOS: la misma interfaz con el visor web del sistema
// (WebView2 en Windows, WebKit en macOS).

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gen2brain/beeep"
	"github.com/rs/zerolog"
	webview "github.com/webview/webview_go"

	"hilvan/assets"
	"hilvan/internal/core"
	"hilvan/internal/server"
)

func init() {
	// La ventana tiene que vivir en el hilo principal del proceso.
	runtime.LockOSThread()
}

func shellAvailable() bool { return true }

type instance struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

func instancePath() string {
	return filepath.Join(core.DefaultPaths("").Config, "instancia.json")
}

// shellInit: si Hilvan ya está abierto, le pide que se muestre (y le pasa el enlace matrix:, si
// hay) y devuelve true para que esta copia termine.
func shellInit(args []string) bool {
	b, err := os.ReadFile(instancePath())
	if err != nil {
		return false
	}
	var in instance
	if json.Unmarshal(b, &in) != nil || !strings.HasPrefix(in.URL, "http://127.0.0.1:") {
		return false
	}
	uri := ""
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "matrix:") || strings.HasPrefix(a, "https://matrix.to/") {
			uri = a
		}
	}
	req, _ := http.NewRequest(http.MethodPost, in.URL+"/present", strings.NewReader(uri))
	req.Header.Set("X-Hilvan-Key", in.Key)
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

func runShell(m *core.Manager, srv *server.Server, log zerolog.Logger, dev bool, openURI string) {
	w := webview.New(dev)
	defer w.Destroy()
	w.SetTitle("Hilvan")
	w.SetSize(1200, 800, webview.HintNone)

	// Archivo privado con la dirección local y una clave para la próxima copia que se abra.
	key := make([]byte, 24)
	_, _ = rand.Read(key)
	in := instance{URL: srv.URL, Key: base64.RawURLEncoding.EncodeToString(key)}
	if b, err := json.Marshal(in); err == nil {
		_ = os.MkdirAll(filepath.Dir(instancePath()), 0o700)
		_ = os.WriteFile(instancePath(), b, 0o600)
		defer os.Remove(instancePath())
	}
	srv.PresentKey = in.Key
	srv.OnPresent = func(uri string) {
		w.Dispatch(func() { bringToFront(w) })
		if uri != "" {
			m.Broadcast(core.Push{Ev: "uri", D: uri})
		}
	}

	m.OpenURL = openBrowser
	m.OnActivate = func() { w.Dispatch(func() { bringToFront(w) }) }
	m.Quit = func() { w.Dispatch(w.Terminate) }
	m.PickSavePath = saveDialog
	m.Notifier = newDesktopNotifier(m)

	w.Navigate(srv.LaunchURL())
	if openURI != "" {
		go func() {
			time.Sleep(3 * time.Second)
			m.Broadcast(core.Push{Ev: "uri", D: openURI})
		}()
	}
	log.Info().Msg("Ventana lista")
	w.Run()
}

// desktopNotifier muestra notificaciones nativas (centro de notificaciones de Windows o macOS).
type desktopNotifier struct{ icon string }

func newDesktopNotifier(m *core.Manager) desktopNotifier {
	p := filepath.Join(m.Paths.Cache, "hilvan-notificacion.png")
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if b, err := os.ReadFile(p); err != nil || !bytes.Equal(b, assets.Icon256) {
		_ = os.WriteFile(p, assets.Icon256, 0o600)
	}
	beeep.AppName = "Hilvan"
	return desktopNotifier{icon: p}
}

func (n desktopNotifier) Notify(a *core.Account, it core.NotifyItem) {
	_ = beeep.Notify(it.Title, it.Body, n.icon)
}

func (n desktopNotifier) Close(account, room string) {}
