//go:build linux

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"sync"
	"time"

	"hilvan/assets"
	"hilvan/internal/core"
	"hilvan/internal/shell"
	"hilvan/internal/tray"
)

const (
	menuToggle int32 = 1
	menuSep    int32 = 2
	menuQuit   int32 = 3
)

// trayHost mantiene el ícono de la bandeja: mostrar/ocultar la ventana, salir y el punto
// de mensajes sin leer.
type trayHost struct {
	m    *core.Manager
	mu   sync.Mutex
	t    *tray.Tray
	kick chan struct{}
	stop chan struct{}
}

func newTrayHost(m *core.Manager) *trayHost {
	h := &trayHost{m: m, kick: make(chan struct{}, 1), stop: make(chan struct{})}
	m.OnPush(func(p core.Push) {
		switch p.Ev {
		case "sync", "accounts", "account.ready", "settings":
			h.poke()
		}
	})
	shell.OnVisible(func(bool) { h.updateMenu() })
	m.TrayChanged = h.enable
	m.AppIconChanged = h.setIcon
	m.TrayAvailable = h.available
	if m.Settings().TrayIcon {
		h.enable(true)
	}
	go h.loop()
	return h
}

func (h *trayHost) icon(custom []byte) image.Image {
	if custom == nil {
		custom = h.m.AppIconPNG()
	}
	if custom != nil {
		if img, err := core.DecodeIcon(custom); err == nil {
			return img
		}
	}
	img, _ := png.Decode(bytes.NewReader(assets.Icon256))
	return img
}

func (h *trayHost) menu() []tray.MenuItem {
	label := "Mostrar Hilvan"
	if shell.Visible() {
		label = "Ocultar Hilvan"
	}
	return []tray.MenuItem{
		{ID: menuToggle, Label: label},
		{ID: menuSep, Separator: true},
		{ID: menuQuit, Label: "Salir de Hilvan"},
	}
}

func (h *trayHost) enable(on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if on && h.t == nil {
		t, err := tray.New("Hilvan", h.icon(nil), h.menu())
		if err != nil {
			h.m.Log.Warn().Err(err).Msg("No se pudo mostrar el ícono en la bandeja")
			return
		}
		t.OnActivate = func(token string) { shell.Toggle(token) }
		t.OnMenu = h.onMenu
		h.t = t
		h.poke()
	} else if !on && h.t != nil {
		h.t.Close()
		h.t = nil
	}
}

func (h *trayHost) get() *tray.Tray {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.t
}

func (h *trayHost) available() bool { return h.get().Available() }

func (h *trayHost) setIcon(png []byte) {
	if t := h.get(); t != nil {
		if png == nil {
			png = assets.Icon256
		}
		t.SetIcon(h.icon(png))
	}
}

func (h *trayHost) updateMenu() {
	if t := h.get(); t != nil {
		t.SetMenu(h.menu())
	}
}

func (h *trayHost) onMenu(id int32) {
	switch id {
	case menuToggle:
		if shell.Visible() {
			shell.Hide()
		} else {
			shell.Show("")
		}
	case menuQuit:
		if h.m.Quit != nil {
			h.m.Quit()
		}
	}
}

func (h *trayHost) poke() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// loop recalcula los no leídos como mucho una vez por segundo (y cada minuto por las dudas).
func (h *trayHost) loop() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-h.kick:
			time.Sleep(time.Second)
		case <-tick.C:
		}
		t := h.get()
		if t == nil {
			continue
		}
		chats, mentions := h.m.UnreadSummary()
		text := "Sin mensajes nuevos"
		switch {
		case chats == 1:
			text = "1 chat sin leer"
		case chats > 1:
			text = fmt.Sprintf("%d chats sin leer", chats)
		}
		if mentions == 1 {
			text += " · 1 mención"
		} else if mentions > 1 {
			text += fmt.Sprintf(" · %d menciones", mentions)
		}
		t.SetBadge(chats > 0, "Hilvan", text)
	}
}

func (h *trayHost) close() {
	close(h.stop)
	h.enable(false)
}
