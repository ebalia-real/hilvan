//go:build linux

package main

import (
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/rs/zerolog"

	"hilvan/internal/core"
	"hilvan/internal/server"
	"hilvan/internal/shell"
)

func shellAvailable() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != ""
}

// shellInit devuelve true si ya hay otra instancia abierta (se le pasó el control).
func shellInit(args []string) bool { return shell.Init(args) }

// videoHost dibuja el video de las llamadas como capa nativa de GTK.
type videoHost struct{}

func (videoHost) Attach(stream string, p unsafe.Pointer) { shell.VideoAttach(stream, p) }
func (videoHost) Place(stream string, x, y, w, h float64, visible, mirror, contain bool) {
	shell.VideoPlace(stream, x, y, w, h, visible, mirror, contain)
}
func (videoHost) Clear() { shell.VideoClear() }

func runShell(m *core.Manager, srv *server.Server, log zerolog.Logger, _ bool, openURI string) {
	m.Video = videoHost{}
	if p := os.Getenv("HILVAN_SNAPSHOT"); p != "" {
		m.Snapshot = func() { shell.WindowSnapshot(strings.TrimSuffix(p, ".png") + "-win.png") }
		// Diagnóstico: mapa de memoria actualizado (para ubicar fallos en bibliotecas nativas).
		go func() {
			for range time.Tick(time.Second) {
				if b, err := os.ReadFile("/proc/self/maps"); err == nil {
					_ = os.WriteFile(strings.TrimSuffix(p, ".png")+"-maps.txt", b, 0o600)
				}
			}
		}()
	}
	m.OpenURL = shell.OpenURI
	m.OnActivate = shell.Present
	m.SetDark = shell.SetDark
	m.Quit = shell.Quit
	m.PickSavePath = shell.SaveDialog
	m.SetBackground = shell.SetBackground
	shell.SetBackground(m.Settings().CloseToTray)
	tr := newTrayHost(m)
	defer tr.close()
	switch m.Settings().Theme {
	case "dark":
		shell.SetDark(1)
	case "light":
		shell.SetDark(0)
	}
	shell.Run(shell.Options{
		HomeURL:   srv.URL,
		LaunchURL: srv.LaunchURL(),
		DataDir:   m.Paths.Data,
		ConfigDir: m.Paths.Config,
		// Herramientas de desarrollo de WebKit solo con HILVAN_DEVTOOLS=1.
		Dev:     os.Getenv("HILVAN_DEVTOOLS") == "1",
		OpenURI: openURI,
		// Inicio automático "oculto": solo si queda algo para volver (la bandeja o el lanzador).
		Hidden: startHidden && (m.Settings().TrayIcon || m.Settings().CloseToTray),
	}, shell.Callbacks{
		Ready: func() { log.Info().Msg("Ventana lista") },
		OpenExt: func(u string) {
			if strings.HasPrefix(u, "https://matrix.to/") {
				m.Broadcast(core.Push{Ev: "uri", D: u})
				return
			}
			shell.OpenURI(u)
		},
		ActivateURI: func(u string) {
			shell.Present()
			m.Broadcast(core.Push{Ev: "uri", D: u})
		},
		Download: func(path string, ok bool) {
			m.Broadcast(core.Push{Ev: "download", D: map[string]any{"path": path, "ok": ok}})
		},
	})
}
