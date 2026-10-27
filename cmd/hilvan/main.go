package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"hilvan/internal/core"
	"hilvan/internal/server"
	"hilvan/web"
)

var version = "0.1.0-dev"

// packaging lo fija cada paquete al compilar (tar, deb, flatpak, nix, windows, macos): define cómo
// se actualiza Hilvan.
var packaging = "dev"

// startHidden: iniciar con la ventana oculta (modo segundo plano / inicio automático).
var startHidden bool

func main() {
	dataDir := flag.String("data", "", "directorio de datos alternativo (perfil aislado)")
	headless := flag.Bool("headless", false, "no abrir ventana; solo servir la interfaz en 127.0.0.1")
	port := flag.Int("port", 0, "puerto local (0 = aleatorio)")
	devAssets := flag.String("dev-assets", "", "servir la interfaz desde este directorio (desarrollo)")
	verbose := flag.Bool("v", false, "registro detallado")
	background := flag.Bool("background", false, "iniciar sin mostrar la ventana (para el inicio automático)")
	showVersion := flag.Bool("version", false, "mostrar versión")
	sbom := flag.Bool("sbom", false, "imprimir la lista de componentes (SBOM CycloneDX) y salir")
	flag.Parse()
	core.Packaging = packaging
	core.CleanOldBinary()
	if *sbom {
		os.Stdout.Write(core.SBOM(version))
		fmt.Println()
		return
	}
	if *showVersion {
		fmt.Println("Hilvan", version)
		return
	}

	level := zerolog.InfoLevel
	if *verbose {
		level = zerolog.DebugLevel
	}
	// Consola + registro reciente en memoria (solo para informes que el usuario decide enviar).
	ring := core.NewLogRing(3000)
	console := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}
	ringW := zerolog.ConsoleWriter{Out: ring, TimeFormat: "2006-01-02 15:04:05", NoColor: true}
	log := zerolog.New(zerolog.MultiLevelWriter(console, ringW)).Level(level).With().Timestamp().Logger()

	// Enlace matrix: recibido como argumento (desde el escritorio o el navegador).
	openURI := ""
	for _, a := range flag.Args() {
		if strings.HasPrefix(a, "matrix:") || strings.HasPrefix(a, "https://matrix.to/") {
			openURI = a
		}
	}
	useShell := !*headless && shellAvailable()
	if useShell && shellInit(os.Args) {
		// Ya había una ventana abierta: la traemos al frente y salimos.
		return
	}

	paths := core.DefaultPaths(*dataDir)
	m, err := core.NewManager(paths, version, log)
	if err != nil {
		log.Fatal().Err(err).Msg("No se pudo iniciar")
	}
	m.LogRing = ring

	var assets fs.FS
	assets, _ = fs.Sub(web.Assets, ".")
	if *devAssets != "" {
		abs, _ := filepath.Abs(*devAssets)
		assets = os.DirFS(abs)
	}
	srv, err := server.New(m, assets, *port, log.With().Str("component", "http").Logger())
	if err != nil {
		log.Fatal().Err(err).Msg("No se pudo abrir el servidor local")
	}
	srv.Dev = *devAssets != ""
	// Llamadas con GStreamer: con ventana, el video se dibuja nativo; sin ventana, solo audio.
	m.Calls.EnableNativeCalls(!useShell)
	go func() {
		if err := srv.Serve(); err != nil {
			log.Error().Err(err).Msg("Servidor local detenido")
		}
	}()
	m.Start()
	go func() {
		limit := func() int64 { return int64(max(m.Settings().MediaCacheMB, 100)) << 20 }
		m.CleanMediaCache(limit())
		t := time.NewTicker(6 * time.Hour)
		for range t.C {
			m.CleanMediaCache(limit())
		}
	}()

	stop := func() {
		srv.Shutdown()
		m.Stop()
	}
	if !useShell {
		fmt.Println(srv.LaunchURL())
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		m.Quit = func() { sig <- syscall.SIGTERM }
		<-sig
		stop()
		return
	}
	startHidden = *background
	log.Debug().Str("url", srv.LaunchURL()).Msg("URL de arranque (solo diagnóstico)")
	runShell(m, srv, log, *devAssets != "", openURI)
	stop()
}
