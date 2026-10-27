package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func testPNG(t *testing.T, w, h int, c color.Color) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAppIcon(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home-data")
	sys := filepath.Join(dir, "sistema")
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_DATA_DIRS", sys)
	t.Setenv("HILVAN_NO_KEYRING", "1")
	_ = os.MkdirAll(filepath.Join(sys, "applications"), 0o755)
	system := "[Desktop Entry]\nName=Hilvan\nExec=/opt/hilvan/bin/hilvan %u\nIcon=io.github.heelxel.Hilvan\nIcon[es]=otro\n\n[Desktop Action nueva]\nName=Nueva\nExec=hilvan --new\n"
	if err := os.WriteFile(filepath.Join(sys, "applications", desktopID()), []byte(system), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := Paths{Data: filepath.Join(dir, "data"), Config: filepath.Join(dir, "config"), Cache: filepath.Join(dir, "cache")}
	m, err := NewManager(paths, "test", zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	var changed [][]byte
	m.AppIconChanged = func(b []byte) { changed = append(changed, b) }
	override := filepath.Join(home, "applications", desktopID())

	// Validación.
	if err := m.SetAppIcon("ocean", nil, testPNG(t, 128, 64, color.White)); err == nil {
		t.Fatal("aceptó un ícono que no es cuadrado")
	}
	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, image.NewRGBA(image.Rect(0, 0, 128, 128)), nil)
	if err := m.SetAppIcon("ocean", nil, jb.Bytes()); err == nil {
		t.Fatal("aceptó un JPEG")
	}

	// Elegir un estilo.
	blue := testPNG(t, 256, 256, color.NRGBA{0, 0x80, 0xff, 0xff})
	if err := m.SetAppIcon("ocean", json.RawMessage(`{"shape":"circle"}`), blue); err != nil {
		t.Fatal(err)
	}
	first := m.AppIconPath()
	if first == "" || !bytes.Equal(m.AppIconPNG(), blue) {
		t.Fatal("no se guardó el ícono")
	}
	s := m.Settings()
	if s.AppIcon != "ocean" || !strings.Contains(string(s.AppIconCfg), "circle") {
		t.Fatalf("ajustes: %q %s", s.AppIcon, s.AppIconCfg)
	}
	icon48 := filepath.Join(home, "icons", "hicolor", "48x48", "apps", defaultAppID+".png")
	center := func() color.NRGBA {
		b, err := os.ReadFile(icon48)
		if err != nil {
			t.Fatal("falta el ícono en el tema del usuario")
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		return color.NRGBAModel.Convert(img.At(24, 24)).(color.NRGBA)
	}
	if c := center(); c.B != 0xff {
		t.Fatalf("ícono del tema: %v", c)
	}
	// No se crea ni se cambia ningún lanzador: solo un archivo oculto pasajero que avisa a GNOME.
	apps := filepath.Join(home, "applications")
	if _, err := os.Stat(override); !os.IsNotExist(err) {
		t.Fatal("no debería crear un lanzador propio")
	}
	if n, _ := filepath.Glob(filepath.Join(apps, ".hilvan-icono-*")); len(n) != 1 {
		t.Fatalf("aviso al escritorio: %v", n)
	}
	time.Sleep(1700 * time.Millisecond)
	if n, _ := filepath.Glob(filepath.Join(apps, ".hilvan-icono-*")); len(n) != 0 {
		t.Fatalf("quedó el aviso: %v", n)
	}

	// Cambiar de estilo: el archivo viejo se borra, el nombre cambia y el tema tiene el nuevo.
	red := testPNG(t, 256, 256, color.NRGBA{0xff, 0, 0, 0xff})
	if err := m.SetAppIcon("sunset", nil, red); err != nil {
		t.Fatal(err)
	}
	if second := m.AppIconPath(); second == first || second == "" {
		t.Fatal("el ícono nuevo debería tener otro nombre")
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("quedó el ícono anterior")
	}
	if c := center(); c.R != 0xff || c.B != 0 {
		t.Fatalf("el tema no tiene el ícono nuevo: %v", c)
	}

	// Volver al clásico: se borra todo lo propio.
	if err := m.SetAppIcon("classic", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(icon48); !os.IsNotExist(err) {
		t.Fatal("quedó el ícono en el tema")
	}
	if m.AppIconPath() != "" || m.Settings().AppIcon != "" {
		t.Fatal("no volvió al clásico")
	}
	if len(changed) != 3 || changed[2] != nil {
		t.Fatalf("avisos de cambio: %d", len(changed))
	}

	// El lanzador propio de la versión anterior se quita; uno hecho a mano no se toca.
	_ = os.WriteFile(override, []byte("[Desktop Entry]\nName=Hilvan\nIcon=/x.png\n"+desktopMarker+"\n"), 0o644)
	m.syncAppIcon()
	if _, err := os.Stat(override); !os.IsNotExist(err) {
		t.Fatal("quedó el lanzador de la versión anterior")
	}
	manual := "[Desktop Entry]\nName=Mi Hilvan\nExec=hilvan\nIcon=/mio.png\n"
	_ = os.WriteFile(override, []byte(manual), 0o644)
	_ = m.SetAppIcon("ocean", nil, blue)
	_ = m.SetAppIcon("", nil, nil)
	if b, _ := os.ReadFile(override); string(b) != manual {
		t.Fatal("se tocó un lanzador hecho a mano")
	}
	_ = os.Remove(override)

	// Al arrancar: si falta el archivo del ícono, vuelve al clásico (y se limpia el tema).
	_ = m.SetAppIcon("ocean", nil, blue)
	_ = os.Remove(m.AppIconPath())
	m.syncAppIcon()
	if m.Settings().AppIcon != "" {
		t.Fatal("sin archivo debería volver al clásico")
	}
	if _, err := os.Stat(icon48); !os.IsNotExist(err) {
		t.Fatal("quedó el ícono en el tema")
	}
}

func TestAppIconPortable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "home-data"))
	t.Setenv("HILVAN_NO_KEYRING", "1")
	m, err := NewManager(DefaultPaths(filepath.Join(dir, "perfil")), "test", zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetAppIcon("ocean", nil, testPNG(t, 128, 128, color.White)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "home-data", "icons")); !os.IsNotExist(err) {
		t.Fatal("un perfil aparte (--data) no debería tocar el ícono de la app instalada")
	}
}

func TestThemeIcon(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("XDG_DATA_DIRS", filepath.Join(dir, "nada"))
	t.Setenv("HILVAN_NO_KEYRING", "1")
	theme := filepath.Join(dir, "icons", "hicolor")
	// Un ícono ajeno (por ejemplo de una instalación a mano) no se pisa ni se borra.
	foreign := filepath.Join(theme, "64x64", "apps", defaultAppID+".png")
	_ = os.MkdirAll(filepath.Dir(foreign), 0o755)
	_ = os.WriteFile(foreign, testPNG(t, 64, 64, color.Black), 0o644)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(theme, old, old)

	m, err := NewManager(Paths{Data: filepath.Join(dir, "data"), Config: filepath.Join(dir, "config"), Cache: filepath.Join(dir, "cache")}, "test", zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetAppIcon("forest", nil, testPNG(t, 512, 512, color.NRGBA{0, 0xa0, 0x50, 0xff})); err != nil {
		t.Fatal(err)
	}
	for _, s := range themeSizes {
		f := filepath.Join(theme, fmt.Sprintf("%dx%d", s, s), "apps", defaultAppID+".png")
		if f == foreign {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("falta %s", f)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != s || !bytes.Contains(b, []byte(themeMarker)) {
			t.Fatalf("ícono de %d px mal hecho: %v", s, err)
		}
		if c := color.NRGBAModel.Convert(img.At(s/2, s/2)).(color.NRGBA); c.G != 0xa0 {
			t.Fatalf("color %v", c)
		}
	}
	if sc, err := os.ReadFile(filepath.Join(theme, "scalable", "apps", defaultAppID+".png")); err != nil || !bytes.Contains(sc, []byte(themeMarker)) {
		t.Fatalf("falta el escalable: %v", err)
	}
	if b, _ := os.ReadFile(foreign); bytes.Contains(b, []byte(themeMarker)) {
		t.Fatal("se pisó un ícono ajeno")
	}
	// La carpeta del tema queda con fecha nueva: así GTK y GNOME Shell lo releen.
	if st, _ := os.Stat(theme); !st.ModTime().After(old.Add(time.Minute)) {
		t.Fatal("no se avisó el cambio del tema")
	}

	if err := m.SetAppIcon("classic", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, f := range themeIconFiles(defaultAppID) {
		if f == foreign {
			continue
		}
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("quedó %s", f)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("se borró un ícono ajeno")
	}
}
