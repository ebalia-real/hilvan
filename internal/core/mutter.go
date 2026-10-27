package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"hilvan/internal/media"
)

// En GNOME podemos listar los monitores y capturarlos directamente (como el selector de Discord),
// con la API de captura de Mutter. Las ventanas, por privacidad, solo con el selector del sistema.

const (
	mutterSC      = "org.gnome.Mutter.ScreenCast"
	mutterSCPath  = "/org/gnome/Mutter/ScreenCast"
	mutterDisplay = "org.gnome.Mutter.DisplayConfig"
)

// ScreenSource es una pantalla que se puede transmitir.
type ScreenSource struct {
	ID        string  `json:"id"` // monitor:<conector>
	Name      string  `json:"name"`
	Connector string  `json:"connector"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Refresh   float64 `json:"refresh"`
	Primary   bool    `json:"primary"`
	Thumb     string  `json:"thumb,omitempty"` // data:image/jpeg;base64,...
}

type dcMode struct {
	ID      string
	W, H    int32
	Refresh float64
	Scale   float64
	Scales  []float64
	Props   map[string]dbus.Variant
}

type dcMonitor struct {
	Spec  struct{ Connector, Vendor, Product, Serial string }
	Modes []dcMode
	Props map[string]dbus.Variant
}

type dcLogical struct {
	X, Y      int32
	Scale     float64
	Transform uint32
	Primary   bool
	Monitors  []struct{ Connector, Vendor, Product, Serial string }
	Props     map[string]dbus.Variant
}

func mutterAvailable(conn *dbus.Conn) bool {
	var owner string
	err := conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, mutterSC).Store(&owner)
	return err == nil && owner != ""
}

// listMonitors devuelve los monitores encendidos (GNOME).
func listMonitors() ([]ScreenSource, error) {
	conn, err := dbus.SessionBus()
	if err != nil || !mutterAvailable(conn) {
		return nil, errors.New("sin captura directa")
	}
	var serial uint32
	var monitors []dcMonitor
	var logical []dcLogical
	var props map[string]dbus.Variant
	call := conn.Object(mutterDisplay, "/org/gnome/Mutter/DisplayConfig").Call(mutterDisplay+".GetCurrentState", 0)
	if call.Err != nil {
		return nil, call.Err
	}
	if err := call.Store(&serial, &monitors, &logical, &props); err != nil {
		return nil, err
	}
	primary := map[string]bool{}
	for _, l := range logical {
		for _, m := range l.Monitors {
			if l.Primary {
				primary[m.Connector] = true
			}
		}
	}
	var out []ScreenSource
	for _, m := range monitors {
		var cur *dcMode
		for i := range m.Modes {
			if v, ok := m.Modes[i].Props["is-current"]; ok {
				if b, _ := v.Value().(bool); b {
					cur = &m.Modes[i]
				}
			}
		}
		if cur == nil {
			continue // apagado
		}
		name := m.Spec.Vendor + " " + m.Spec.Product
		if v, ok := m.Props["display-name"]; ok {
			if s, _ := v.Value().(string); s != "" {
				name = s
			}
		}
		out = append(out, ScreenSource{ID: "monitor:" + m.Spec.Connector, Name: strings.TrimSpace(name), Connector: m.Spec.Connector,
			Width: int(cur.W), Height: int(cur.H), Refresh: cur.Refresh, Primary: primary[m.Spec.Connector]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Primary && !out[j].Primary })
	return out, nil
}

// startMutterMonitor captura un monitor sin pasar por el selector (el usuario ya lo eligió en Hilvan).
func startMutterMonitor(connector string, cursor bool) (*screenCast, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*screenCast, error) { conn.Close(); return nil, e }
	var session dbus.ObjectPath
	if err := conn.Object(mutterSC, mutterSCPath).Call(mutterSC+".CreateSession", 0, map[string]dbus.Variant{}).Store(&session); err != nil {
		return fail(err)
	}
	mode := uint32(0) // 0 oculto, 1 incrustado, 2 metadatos
	if cursor {
		mode = 1
	}
	var stream dbus.ObjectPath
	if err := conn.Object(mutterSC, session).Call(mutterSC+".Session.RecordMonitor", 0, connector,
		map[string]dbus.Variant{"cursor-mode": dbus.MakeVariant(mode)}).Store(&stream); err != nil {
		return fail(err)
	}
	ch := make(chan *dbus.Signal, 8)
	conn.Signal(ch)
	_ = conn.AddMatchSignal(dbus.WithMatchObjectPath(stream), dbus.WithMatchInterface(mutterSC+".Stream"), dbus.WithMatchMember("PipeWireStreamAdded"))
	_ = conn.AddMatchSignal(dbus.WithMatchObjectPath(session), dbus.WithMatchInterface(mutterSC+".Session"), dbus.WithMatchMember("Closed"))
	if call := conn.Object(mutterSC, session).Call(mutterSC+".Session.Start", 0); call.Err != nil {
		return fail(call.Err)
	}
	sc := &screenCast{conn: conn, mutter: session, fd: -1}
	timeout := time.After(5 * time.Second)
	for sc.node == 0 {
		select {
		case sig := <-ch:
			if sig.Path == stream && strings.HasSuffix(sig.Name, ".PipeWireStreamAdded") && len(sig.Body) > 0 {
				sc.node, _ = sig.Body[0].(uint32)
			}
		case <-timeout:
			sc.Close()
			return nil, errors.New("GNOME no inició la captura del monitor")
		}
	}
	go func() {
		for sig := range ch {
			if sig.Path == session && strings.HasSuffix(sig.Name, ".Session.Closed") {
				if !sc.closed && sc.onClosed != nil {
					sc.onClosed()
				}
				return
			}
		}
	}()
	return sc, nil
}

var thumbMu sync.Mutex

// monitorThumb toma una miniatura JPEG de un monitor (para el selector de transmisión).
func monitorThumb(connector string, width, height int) (string, error) {
	thumbMu.Lock()
	defer thumbMu.Unlock()
	sc, err := startMutterMonitor(connector, false)
	if err != nil {
		return "", err
	}
	defer sc.Close()
	f, err := os.CreateTemp("", "hilvan-thumb-*.jpg")
	if err != nil {
		return "", err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if err := media.Snapshot(int(sc.node), width, height, path, 3*time.Second); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil || len(b) == 0 {
		return "", fmt.Errorf("miniatura vacía")
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b), nil
}

// screenSources lista los monitores con miniaturas (en paralelo, con tope de tiempo).
func screenSources(ctx context.Context, thumbs bool) map[string]any {
	mons, err := listMonitors()
	res := map[string]any{"direct": err == nil, "monitors": []ScreenSource{}}
	if err != nil {
		return res
	}
	if thumbs {
		var mu sync.Mutex
		got := make([]string, len(mons))
		var wg sync.WaitGroup
		for i := range mons {
			wg.Add(1)
			h := 216
			if mons[i].Width > 0 {
				h = 384 * mons[i].Height / mons[i].Width
			}
			go func(i int, connector string, h int) {
				defer wg.Done()
				if t, err := monitorThumb(connector, 384, h&^1); err == nil {
					mu.Lock()
					got[i] = t
					mu.Unlock()
				}
			}(i, mons[i].Connector, h)
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
		case <-time.After(8 * time.Second):
		}
		mu.Lock()
		for i := range mons {
			mons[i].Thumb = got[i]
		}
		mu.Unlock()
	}
	res["monitors"] = mons
	return res
}
