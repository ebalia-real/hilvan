//go:build android

// hilvan-android es el motor de Hilvan como biblioteca para la app de Android (libhilvan.so):
// la app (Java) lo arranca, muestra la interfaz en un WebView y recibe de acá notificaciones,
// enlaces y pedidos de "Guardar como".
package main

/*
#cgo LDFLAGS: -llog
#include <stdlib.h>
char *hv_java_call(const char *kind, const char *json);
*/
import "C"

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/rs/zerolog"

	"hilvan/internal/core"
	"hilvan/internal/server"
	"hilvan/web"
)

var version = "0.1.0-android"

func main() {}

func init() { core.Packaging = "android" }

var (
	mu  sync.Mutex
	mgr *core.Manager
	srv *server.Server
	url string
)

// javaCall llama a Core.onCall(kind, json) en Java y devuelve su respuesta.
func javaCall(kind string, v any) string {
	b, _ := json.Marshal(v)
	ck, cj := C.CString(kind), C.CString(string(b))
	defer C.free(unsafe.Pointer(ck))
	defer C.free(unsafe.Pointer(cj))
	r := C.hv_java_call(ck, cj)
	if r == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(r))
	return C.GoString(r)
}

// androidNotifier muestra las notificaciones con el sistema de Android.
type androidNotifier struct{}

func (androidNotifier) Notify(a *core.Account, n core.NotifyItem) {
	javaCall("notify", map[string]any{"account": a.UserID(), "room": n.Room, "event": n.EventID,
		"title": n.Title, "body": n.Body, "kind": n.Kind, "urgent": n.Urgent})
}

func (androidNotifier) Close(account, room string) {
	javaCall("cancel", map[string]string{"account": account, "room": room})
}

// logWriter manda el registro a logcat (y al anillo para informes).
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	javaCall("log", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

//export hvStart
func hvStart(cdata, cdownloads, cmodel *C.char) *C.char {
	mu.Lock()
	defer mu.Unlock()
	if mgr != nil {
		return C.CString(url)
	}
	data, downloads, model := C.GoString(cdata), C.GoString(cdownloads), C.GoString(cmodel)
	tmp := filepath.Join(data, "tmp")
	_ = os.MkdirAll(tmp, 0o700)
	os.Setenv("TMPDIR", tmp)
	os.Setenv("HOME", data)
	os.Setenv("HILVAN_NO_KEYRING", "1")
	os.Setenv("HILVAN_DOWNLOAD_DIR", downloads)
	os.Setenv("HILVAN_DEVICE_MODEL", model)

	ring := core.NewLogRing(3000)
	out := zerolog.ConsoleWriter{Out: logWriter{}, NoColor: true, TimeFormat: "15:04:05"}
	rw := zerolog.ConsoleWriter{Out: ring, NoColor: true, TimeFormat: "2006-01-02 15:04:05"}
	log := zerolog.New(zerolog.MultiLevelWriter(out, rw)).Level(zerolog.InfoLevel).With().Timestamp().Logger()

	m, err := core.NewManager(core.DefaultPaths(filepath.Join(data, "perfil")), version, log)
	if err != nil {
		log.Error().Err(err).Msg("No se pudo iniciar")
		return C.CString("")
	}
	m.LogRing = ring
	m.Notifier = androidNotifier{}
	m.OpenURL = func(u string) { javaCall("openUrl", u) }
	m.PickSavePath = func(title, name, folder string) (string, error) {
		return javaCall("saveDialog", map[string]string{"title": title, "name": name}), nil
	}
	m.ExportFile = func(src, dest, name, mime string) (string, error) {
		r := javaCall("export", map[string]string{"src": src, "dest": dest, "name": name, "mime": mime})
		if strings.HasPrefix(r, "error:") {
			return "", &exportErr{strings.TrimPrefix(r, "error:")}
		}
		return r, nil
	}
	m.Quit = func() { javaCall("quit", nil) }
	m.LauncherIcon = func(style string) { javaCall("launcherIcon", style) }
	m.InstallPackage = func(path string) error {
		if r := javaCall("installApk", path); strings.HasPrefix(r, "error:") {
			return &exportErr{strings.TrimPrefix(r, "error:")}
		}
		return nil
	}
	m.Platform = func(action string) string { return javaCall("platform", action) }
	m.OnSettings = func(s core.Settings) {
		javaCall("settings", map[string]bool{"background": s.CloseToTray, "boot": s.Autostart})
	}
	// Primera vez en el teléfono: que se reconecte solo al prenderlo (como cualquier mensajero).
	if first := filepath.Join(data, "android-init"); !fileExists(first) {
		_, _ = m.UpdateSettings([]byte(`{"autostart":true,"close_to_tray":true}`))
		_ = os.WriteFile(first, []byte("1"), 0o600)
	} else {
		m.OnSettings(m.Settings())
	}

	assets, _ := fs.Sub(web.Assets, ".")
	s, err := server.New(m, assets, 0, log.With().Str("component", "http").Logger())
	if err != nil {
		log.Error().Err(err).Msg("No se pudo abrir el servidor local")
		return C.CString("")
	}
	go func() { _ = s.Serve() }()
	m.Start()
	go func() {
		limit := func() int64 { return int64(max(m.Settings().MediaCacheMB, 100)) << 20 }
		m.CleanMediaCache(limit())
		for range time.Tick(6 * time.Hour) {
			m.CleanMediaCache(limit())
		}
	}()
	mgr, srv, url = m, s, s.LaunchURL()
	return C.CString(url)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

type exportErr struct{ msg string }

func (e *exportErr) Error() string { return e.msg }

//export hvStop
func hvStop() {
	mu.Lock()
	defer mu.Unlock()
	if mgr == nil {
		return
	}
	srv.Shutdown()
	mgr.Stop()
	mgr, srv, url = nil, nil, ""
}

//export hvOpenRoom
func hvOpenRoom(caccount, croom *C.char) {
	mu.Lock()
	m := mgr
	mu.Unlock()
	if m != nil {
		m.Broadcast(core.Push{Ev: "open", A: C.GoString(caccount), D: map[string]string{"room": C.GoString(croom)}})
	}
}

//export hvOpenURI
func hvOpenURI(curi *C.char) {
	mu.Lock()
	m := mgr
	mu.Unlock()
	if m != nil {
		m.Broadcast(core.Push{Ev: "uri", D: C.GoString(curi)})
	}
}
