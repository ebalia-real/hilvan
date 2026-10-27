package core

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// Compartir pantalla con el portal de escritorio (xdg-desktop-portal ScreenCast):
// el sistema muestra su propio selector y nos entrega un flujo de PipeWire.

const (
	portalDest = "org.freedesktop.portal.Desktop"
	portalPath = "/org/freedesktop/portal/desktop"
	portalSC   = "org.freedesktop.portal.ScreenCast"
)

type screenCast struct {
	conn     *dbus.Conn
	session  dbus.ObjectPath // sesión del portal
	mutter   dbus.ObjectPath // sesión de captura directa de GNOME
	restore  string          // token para volver a compartir lo mismo sin preguntar
	fd       int
	node     uint32
	onClosed func()
	closed   bool
}

func portalToken() string { return fmt.Sprintf("hilvan%d", rand.Uint32()) }

// portalRequest llama a un método del portal y espera su señal Response.
func portalRequest(conn *dbus.Conn, method string, opts map[string]dbus.Variant, args ...any) (map[string]dbus.Variant, error) {
	return portalCall(conn, portalSC, method, opts, args...)
}

// portalCall es portalRequest para cualquier interfaz del portal (captura, selector de archivos…).
func portalCall(conn *dbus.Conn, iface, method string, opts map[string]dbus.Variant, args ...any) (map[string]dbus.Variant, error) {
	token := portalToken()
	opts["handle_token"] = dbus.MakeVariant(token)
	sender := strings.ReplaceAll(strings.TrimPrefix(conn.Names()[0], ":"), ".", "_")
	path := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/portal/desktop/request/%s/%s", sender, token))
	ch := make(chan *dbus.Signal, 4)
	conn.Signal(ch)
	defer conn.RemoveSignal(ch)
	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface("org.freedesktop.portal.Request"), dbus.WithMatchMember("Response")); err != nil {
		return nil, err
	}
	defer conn.RemoveMatchSignal(dbus.WithMatchObjectPath(path), dbus.WithMatchInterface("org.freedesktop.portal.Request"), dbus.WithMatchMember("Response"))
	call := conn.Object(portalDest, portalPath).Call(iface+"."+method, 0, append(args, opts)...)
	if call.Err != nil {
		return nil, call.Err
	}
	timeout := time.After(3 * time.Minute) // el usuario puede tardar en elegir
	for {
		select {
		case sig := <-ch:
			if sig.Path != path || len(sig.Body) < 2 {
				continue
			}
			code, _ := sig.Body[0].(uint32)
			res, _ := sig.Body[1].(map[string]dbus.Variant)
			switch code {
			case 0:
				return res, nil
			case 1:
				return nil, errors.New("cancelado")
			default:
				if iface == portalSC {
					return nil, errors.New("el sistema no permitió compartir la pantalla")
				}
				return nil, errors.New("el sistema no permitió la operación")
			}
		case <-timeout:
			return nil, errors.New("tiempo de espera agotado")
		}
	}
}

// startScreenCast abre el selector del sistema. types: 1 monitores, 2 ventanas, 3 ambos.
// Con restore (token de una vez anterior) el sistema puede reusar la elección sin preguntar.
func startScreenCast(cursor bool, types uint32, restore string) (*screenCast, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, errors.New("no hay bus de sesión para compartir pantalla")
	}
	fail := func(e error) (*screenCast, error) { conn.Close(); return nil, e }
	res, err := portalRequest(conn, "CreateSession", map[string]dbus.Variant{"session_handle_token": dbus.MakeVariant(portalToken())})
	if err != nil {
		return fail(fmt.Errorf("compartir pantalla: %w", err))
	}
	var session dbus.ObjectPath
	if v, ok := res["session_handle"]; ok {
		switch x := v.Value().(type) {
		case string:
			session = dbus.ObjectPath(x)
		case dbus.ObjectPath:
			session = x
		}
	}
	if session == "" {
		return fail(errors.New("el portal no creó la sesión"))
	}
	sc := &screenCast{conn: conn, session: session, fd: -1}
	// Modo del cursor: incrustado (2) u oculto (1), según lo que soporte el portal.
	cursorMode := uint32(1)
	if cursor {
		cursorMode = 2
	}
	if v, err := conn.Object(portalDest, portalPath).GetProperty(portalSC + ".AvailableCursorModes"); err == nil {
		if modes, ok := v.Value().(uint32); ok && modes&cursorMode == 0 {
			cursorMode = 0
		}
	}
	if types == 0 {
		types = 1 | 2
	}
	opts := map[string]dbus.Variant{"types": dbus.MakeVariant(types), "multiple": dbus.MakeVariant(false), "persist_mode": dbus.MakeVariant(uint32(2))}
	if restore != "" {
		opts["restore_token"] = dbus.MakeVariant(restore)
	}
	if cursorMode != 0 {
		opts["cursor_mode"] = dbus.MakeVariant(cursorMode)
	}
	if _, err := portalRequest(conn, "SelectSources", opts, session); err != nil {
		sc.Close()
		return nil, fmt.Errorf("compartir pantalla: %w", err)
	}
	res, err = portalRequest(conn, "Start", map[string]dbus.Variant{}, session, "")
	if err != nil {
		sc.Close()
		return nil, fmt.Errorf("compartir pantalla: %w", err)
	}
	if v, ok := res["restore_token"]; ok {
		sc.restore, _ = v.Value().(string)
	}
	if v, ok := res["streams"]; ok {
		if streams, ok := v.Value().([][]any); ok && len(streams) > 0 && len(streams[0]) > 0 {
			sc.node, _ = streams[0][0].(uint32)
		}
	}
	if sc.node == 0 {
		sc.Close()
		return nil, errors.New("no se eligió ninguna pantalla")
	}
	var fd dbus.UnixFD
	if err := conn.Object(portalDest, portalPath).Call(portalSC+".OpenPipeWireRemote", 0, session, map[string]dbus.Variant{}).Store(&fd); err != nil {
		sc.Close()
		return nil, fmt.Errorf("compartir pantalla: %w", err)
	}
	sc.fd = int(fd)
	// Si el usuario deja de compartir desde el sistema, nos avisa la señal Closed.
	_ = conn.AddMatchSignal(dbus.WithMatchObjectPath(session), dbus.WithMatchInterface("org.freedesktop.portal.Session"), dbus.WithMatchMember("Closed"))
	ch := make(chan *dbus.Signal, 2)
	conn.Signal(ch)
	go func() {
		for sig := range ch {
			if sig.Path == session && sig.Name == "org.freedesktop.portal.Session.Closed" {
				if !sc.closed && sc.onClosed != nil {
					sc.onClosed()
				}
				return
			}
		}
	}()
	return sc, nil
}

func (sc *screenCast) Close() {
	if sc == nil || sc.closed {
		return
	}
	sc.closed = true
	if sc.session != "" {
		sc.conn.Object(portalDest, sc.session).Call("org.freedesktop.portal.Session.Close", 0)
	}
	if sc.mutter != "" {
		sc.conn.Object(mutterSC, sc.mutter).Call(mutterSC+".Session.Stop", 0)
	}
	if sc.fd >= 0 {
		_ = os.NewFile(uintptr(sc.fd), "pipewire").Close()
	}
	sc.conn.Close()
}
