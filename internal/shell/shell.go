//go:build linux

// Package shell es la ventana nativa (GTK4 + WebKitGTK) de Hilvan en Linux.
package shell

/*
#cgo pkg-config: gtk4 webkitgtk-6.0
#include <stdlib.h>
#include "shell.h"
*/
import "C"

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"
)

func init() {
	// GTK debe correr siempre en el hilo principal del proceso.
	runtime.LockOSThread()
}

type Options struct {
	HomeURL   string
	LaunchURL string
	DataDir   string
	ConfigDir string
	Dev       bool
	OpenURI   string
	Hidden    bool
}

type geometry struct {
	W, H      int
	Maximized bool
}

var (
	mu            sync.Mutex
	onReady       func()
	onOpenExt     func(string)
	onActivateURI func(string)
	onDownload    func(path string, ok bool)
	onVisible     func(bool)
	visible       bool
	geomPath      string
)

// Init registra la aplicación; devuelve true si otra instancia ya estaba corriendo
// (en ese caso se le pasó el control y este proceso debe terminar).
func Init(args []string) bool {
	cargs := make([]*C.char, len(args))
	for i, a := range args {
		cargs[i] = C.CString(a)
	}
	defer func() {
		for _, c := range cargs {
			C.free(unsafe.Pointer(c))
		}
	}()
	var argv **C.char
	if len(cargs) > 0 {
		argv = &cargs[0]
	}
	return C.shell_init(C.int(len(args)), argv) == 1
}

type Callbacks struct {
	Ready       func()
	OpenExt     func(string)
	ActivateURI func(string)
	Download    func(path string, ok bool)
}

// Run abre la ventana y bloquea hasta que se cierra.
func Run(o Options, cb Callbacks) int {
	mu.Lock()
	onReady, onOpenExt, onActivateURI, onDownload = cb.Ready, cb.OpenExt, cb.ActivateURI, cb.Download
	geomPath = filepath.Join(o.ConfigDir, "window.json")
	mu.Unlock()
	g := geometry{W: 1200, H: 800}
	if b, err := os.ReadFile(geomPath); err == nil {
		_ = json.Unmarshal(b, &g)
	}
	home := C.CString(o.HomeURL)
	start := C.CString(o.LaunchURL)
	data := C.CString(o.DataDir)
	uri := C.CString(o.OpenURI)
	defer C.free(unsafe.Pointer(home))
	defer C.free(unsafe.Pointer(start))
	defer C.free(unsafe.Pointer(data))
	defer C.free(unsafe.Pointer(uri))
	dev := 0
	if o.Dev {
		dev = 1
	}
	maxd := 0
	if g.Maximized {
		maxd = 1
	}
	hidden := 0
	if o.Hidden {
		hidden = 1
	}
	return int(C.shell_run(home, start, data, C.int(g.W), C.int(g.H), C.int(maxd), C.int(dev), uri, C.int(hidden)))
}

func Present() { C.shell_present() }

// Show muestra la ventana y la trae al frente (token: activación de Wayland, puede ser "").
func Show(token string) {
	c := C.CString(token)
	defer C.free(unsafe.Pointer(c))
	C.shell_show(c)
}

var (
	saveMu   sync.Mutex
	saveNext int
	saveWait = map[int]chan string{}
)

// SaveDialog abre "Guardar como" sobre la ventana y espera la elección ("" si se cancela).
func SaveDialog(title, name, folder string) (string, error) {
	saveMu.Lock()
	saveNext++
	id := saveNext
	ch := make(chan string, 1)
	saveWait[id] = ch
	saveMu.Unlock()
	ct, cn, cf := C.CString(title), C.CString(name), C.CString(folder)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cn))
	defer C.free(unsafe.Pointer(cf))
	C.shell_save_dialog(C.int(id), ct, cn, cf)
	return <-ch, nil
}

//export goShellSaveChosen
func goShellSaveChosen(id C.int, path *C.char) {
	p := C.GoString(path)
	saveMu.Lock()
	ch := saveWait[int(id)]
	delete(saveWait, int(id))
	saveMu.Unlock()
	if ch != nil {
		ch <- p
	}
}

// Hide oculta la ventana (la app sigue en la bandeja).
func Hide() { C.shell_hide() }

// Toggle: oculta la ventana si está al frente; si no, la muestra.
func Toggle(token string) {
	c := C.CString(token)
	defer C.free(unsafe.Pointer(c))
	C.shell_toggle(c)
}

// OnVisible se llama cuando la ventana se muestra u oculta.
func OnVisible(f func(bool)) {
	mu.Lock()
	onVisible = f
	mu.Unlock()
}

// Visible dice si la ventana está a la vista.
func Visible() bool {
	mu.Lock()
	defer mu.Unlock()
	return visible
}

func Quit() { C.shell_quit() }

func OpenURI(u string) {
	c := C.CString(u)
	defer C.free(unsafe.Pointer(c))
	C.shell_open_uri(c)
}

// Eval ejecuta JavaScript en la interfaz (solo para avisos internos).
func Eval(js string) {
	c := C.CString(js)
	defer C.free(unsafe.Pointer(c))
	C.shell_eval(c)
}

// SetBackground: si está activo, cerrar la ventana la oculta y la app sigue sincronizando.
func SetBackground(on bool) {
	v := 0
	if on {
		v = 1
	}
	C.shell_set_background(C.int(v))
}

// SetDark: 1 oscuro, 0 claro, -1 seguir al sistema.
func SetDark(mode int) { C.shell_set_dark(C.int(mode)) }

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// VideoAttach muestra un GdkPaintable (de GStreamer) como capa nativa; toma su referencia.
func VideoAttach(stream string, paintable unsafe.Pointer) {
	cs := C.CString(stream)
	defer C.free(unsafe.Pointer(cs))
	C.shell_video_attach(cs, paintable)
}

// VideoPlace ubica un video en coordenadas CSS de la página.
func VideoPlace(stream string, x, y, w, h float64, visible, mirror, contain bool) {
	cs := C.CString(stream)
	defer C.free(unsafe.Pointer(cs))
	C.shell_video_place(cs, C.double(x), C.double(y), C.double(w), C.double(h), cbool(visible), cbool(mirror), cbool(contain))
}

func VideoClear() { C.shell_video_clear() }

// WindowSnapshot guarda una captura de la ventana completa (solo diagnóstico).
func WindowSnapshot(path string) {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	C.shell_window_snapshot(cs)
}

func SetTitle(t string) {
	c := C.CString(t)
	defer C.free(unsafe.Pointer(c))
	C.shell_set_title(c)
}

//export goShellReady
func goShellReady() {
	mu.Lock()
	f := onReady
	mu.Unlock()
	if f != nil {
		go f()
	}
}

//export goShellClosed
func goShellClosed(w, h, maximized C.int) {
	g := geometry{W: int(w), H: int(h), Maximized: maximized == 1}
	if g.Maximized {
		// Conservamos el último tamaño no maximizado.
		var prev geometry
		if b, err := os.ReadFile(geomPath); err == nil && json.Unmarshal(b, &prev) == nil {
			g.W, g.H = prev.W, prev.H
		}
	}
	if b, err := json.Marshal(g); err == nil {
		_ = os.MkdirAll(filepath.Dir(geomPath), 0o700)
		_ = os.WriteFile(geomPath, b, 0o600)
	}
}

//export goShellVisible
func goShellVisible(v C.int) {
	mu.Lock()
	visible = v == 1
	f := onVisible
	mu.Unlock()
	if f != nil {
		go f(v == 1)
	}
}

//export goShellOpenExternal
func goShellOpenExternal(uri *C.char) {
	u := C.GoString(uri)
	mu.Lock()
	f := onOpenExt
	mu.Unlock()
	if f != nil {
		go f(u)
	}
}

//export goShellActivateURI
func goShellActivateURI(uri *C.char) {
	u := C.GoString(uri)
	mu.Lock()
	f := onActivateURI
	mu.Unlock()
	if f != nil {
		go f(u)
	}
}

//export goShellDownloadDone
func goShellDownloadDone(path *C.char, ok C.int) {
	p := C.GoString(path)
	mu.Lock()
	f := onDownload
	mu.Unlock()
	if f != nil {
		go f(p, ok == 1)
	}
}
