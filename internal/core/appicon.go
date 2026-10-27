package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/image/draw"
)

// Ícono de la app a elección (como los íconos alternativos de Telegram o Discord). La interfaz
// dibuja el estilo elegido (o la imagen propia) en PNG; acá se guarda, se usa en la bandeja y las
// notificaciones, y en Linux se pone en el tema de íconos del usuario (dock, barra de tareas, menú).

const (
	defaultAppID  = "io.github.heelxel.Hilvan"
	desktopMarker = "X-Hilvan-Custom-Icon=true"
	themeMarker   = "hilvan-icono-elegido"
	maxIconBytes  = 4 << 20
)

// appID es el nombre de la app en el escritorio; HILVAN_DEV_APP_ID permite una copia de
// pruebas con su propio lanzador, sin tocar el de la instalada.
func appID() string {
	if v := os.Getenv("HILVAN_DEV_APP_ID"); v != "" {
		return v
	}
	return defaultAppID
}

func desktopID() string { return appID() + ".desktop" }

func (m *Manager) iconDir() string { return filepath.Join(m.Paths.Data, "icono") }

// AppIconPath devuelve el PNG del ícono elegido, o "" si se usa el clásico.
func (m *Manager) AppIconPath() string {
	if m.Settings().AppIcon == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(m.iconDir(), "hilvan-*.png"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// AppIconPNG devuelve el ícono elegido (nil si es el clásico).
func (m *Manager) AppIconPNG() []byte {
	if p := m.AppIconPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			return b
		}
	}
	return nil
}

// SetAppIcon guarda y aplica un ícono. style "" (o "classic") vuelve al original.
func (m *Manager) SetAppIcon(style string, cfg json.RawMessage, data []byte) error {
	if style == "classic" {
		style = ""
	}
	if style != "" {
		if len(data) == 0 || len(data) > maxIconBytes {
			return errors.New("la imagen del ícono no es válida")
		}
		c, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || format != "png" {
			return errors.New("el ícono tiene que ser una imagen PNG")
		}
		if c.Width != c.Height || c.Width < 64 || c.Width > 1024 {
			return errors.New("el ícono tiene que ser cuadrado, de 64 a 1024 píxeles")
		}
	}
	// Se borran los anteriores y el nuevo lleva su huella en el nombre: así el escritorio no
	// muestra uno viejo de su caché.
	old, _ := filepath.Glob(filepath.Join(m.iconDir(), "hilvan-*.png"))
	path, rev := "", ""
	if style != "" {
		sum := sha256.Sum256(data)
		rev = hex.EncodeToString(sum[:6])
		path = filepath.Join(m.iconDir(), "hilvan-"+rev+".png")
		if err := os.MkdirAll(m.iconDir(), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
			return err
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			return err
		}
	}
	for _, o := range old {
		if o != path {
			_ = os.Remove(o)
		}
	}
	patch := map[string]any{"app_icon": style, "app_icon_cfg": nil, "app_icon_rev": rev}
	if style != "" && len(cfg) > 0 && json.Valid(cfg) {
		patch["app_icon_cfg"] = cfg
	}
	b, _ := json.Marshal(patch)
	if _, err := m.settings.Patch(b); err != nil {
		return err
	}
	m.applyAppIcon(path)
	if m.LauncherIcon != nil {
		st := style
		if st == "" {
			st = "classic"
		}
		m.LauncherIcon(st)
	}
	if m.AppIconChanged != nil {
		if style == "" {
			m.AppIconChanged(nil)
		} else {
			m.AppIconChanged(data)
		}
	}
	m.Broadcast(Push{Ev: "settings", D: m.Settings()})
	return nil
}

// syncAppIcon se llama al arrancar: vuelve a poner el ícono en el tema (por si se borró) o vuelve
// al clásico si el archivo del ícono ya no está.
func (m *Manager) syncAppIcon() {
	if m.Settings().AppIcon == "" {
		m.applyAppIcon("")
		return
	}
	p := m.AppIconPath()
	if p == "" {
		_ = m.SetAppIcon("", nil, nil)
		return
	}
	m.applyAppIcon(p)
}

// applyAppIcon lleva el ícono al escritorio. El dock, la barra de tareas, Alt+Tab y el menú de
// aplicaciones muestran el ícono que nombra el lanzador instalado (io.github.heelxel.Hilvan):
// el elegido se pone en el tema de íconos del usuario con ese mismo nombre, que tiene prioridad
// sobre el del sistema, y se avisa al escritorio para que lo relea. Así cambia también en la app
// abierta. (Reemplazar el lanzador no sirve: GNOME deja la ventana abierta con la app vieja y,
// si está anclada, la muestra dos veces.)
func (m *Manager) applyAppIcon(path string) {
	m.setNotifyIcon(path)
	// Un perfil aparte (--data) no toca el ícono de la app instalada.
	if runtime.GOOS != "linux" || (m.Paths.Portable && os.Getenv("HILVAN_DEV_APP_ID") == "") {
		return
	}
	// Lanzador propio de versiones anteriores de esta función: se quita.
	legacy := removeDesktopOverride() == nil && legacyRemoved
	changed := false
	if path == "" {
		changed = removeThemeIcon(appID())
	} else if data, err := os.ReadFile(path); err == nil {
		if err := installThemeIcon(appID(), data); err != nil {
			m.Log.Warn().Err(err).Msg("No se pudo poner el ícono en el tema del usuario")
		}
		changed = true
	}
	if changed || legacy {
		nudgeDesktop()
	}
}

// nudgeDesktop avisa al escritorio que relea las apps (y con eso el tema de íconos). GNOME vigila
// la carpeta de lanzadores: crear y borrar un archivo oculto alcanza, sin cambiar ningún lanzador.
// GNOME tarda unos segundos (agrupa los cambios) y después redibuja el dock.
func nudgeDesktop() {
	dir := filepath.Join(dataHome(), "applications")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	stale, _ := filepath.Glob(filepath.Join(dir, ".hilvan-icono-*"))
	for _, f := range stale {
		_ = os.Remove(f)
	}
	f, err := os.CreateTemp(dir, ".hilvan-icono-*")
	if err != nil {
		return
	}
	f.Close()
	go func() {
		time.Sleep(1500 * time.Millisecond)
		_ = os.Remove(f.Name())
	}()
}

var themeSizes = []int{16, 22, 24, 32, 48, 64, 128, 256, 512}

func iconThemeDir() string { return filepath.Join(dataHome(), "icons", "hicolor") }

func themeIconFiles(id string) []string {
	dir := iconThemeDir()
	var out []string
	for _, s := range themeSizes {
		out = append(out, filepath.Join(dir, fmt.Sprintf("%dx%d", s, s), "apps", id+".png"))
	}
	return append(out, filepath.Join(dir, "scalable", "apps", id+".png"))
}

// ownedOrMissing: solo se pisan archivos que no existen o que puso Hilvan.
func ownedOrMissing(path string) bool {
	b, err := os.ReadFile(path)
	return err != nil || bytes.Contains(b, []byte(themeMarker))
}

func installThemeIcon(id string, data []byte) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	files := themeIconFiles(id)
	// El SVG de una versión anterior (uno ajeno con ese nombre no se toca).
	if old := filepath.Join(iconThemeDir(), "scalable", "apps", id+".svg"); ownedOrMissing(old) {
		_ = os.Remove(old)
	}
	for i, s := range themeSizes {
		if !ownedOrMissing(files[i]) {
			continue
		}
		dst := image.NewNRGBA(image.Rect(0, 0, s, s))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
		var b bytes.Buffer
		if err := png.Encode(&b, dst); err != nil {
			return err
		}
		if err := writeFileAtomic(files[i], pngWithComment(b.Bytes(), themeMarker)); err != nil {
			return err
		}
	}
	// "Escalable": el PNG grande. Le gana al SVG instalado en los tamaños que no tienen archivo
	// propio (por ejemplo el dock a 36 px).
	if sc := files[len(files)-1]; ownedOrMissing(sc) {
		if err := writeFileAtomic(sc, pngWithComment(data, themeMarker)); err != nil {
			return err
		}
	}
	refreshIconTheme()
	return nil
}

func removeThemeIcon(id string) bool {
	removed := false
	// (Incluye el SVG que ponía una versión anterior.)
	legacySVG := filepath.Join(iconThemeDir(), "scalable", "apps", id+".svg")
	for _, f := range append(themeIconFiles(id), legacySVG) {
		if b, err := os.ReadFile(f); err == nil && bytes.Contains(b, []byte(themeMarker)) {
			_ = os.Remove(f)
			removed = true
		}
	}
	if removed {
		refreshIconTheme()
	}
	return removed
}

// refreshIconTheme avisa que el tema cambió: GTK y GNOME Shell miran la fecha de la carpeta
// del tema; si hay caché de íconos, se rehace (como hace xdg-icon-resource).
func refreshIconTheme() {
	dir := iconThemeDir()
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
	if _, err := os.Stat(filepath.Join(dir, "icon-theme.cache")); err != nil {
		return
	}
	if bin, err := exec.LookPath("gtk-update-icon-cache"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, bin, "-q", "-t", "-f", dir).Run()
	}
}

// pngWithComment agrega un bloque de texto (tEXt) después de IHDR: así se reconoce el archivo.
func pngWithComment(b []byte, text string) []byte {
	const ihdrEnd = 8 + 4 + 4 + 13 + 4
	if len(b) < ihdrEnd || string(b[12:16]) != "IHDR" {
		return b
	}
	data := append([]byte("Comment\x00"), text...)
	chunk := make([]byte, 0, 12+len(data))
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(len(data)))
	typed := append([]byte("tEXt"), data...)
	chunk = append(chunk, typed...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(typed))
	out := make([]byte, 0, len(b)+len(chunk))
	out = append(out, b[:ihdrEnd]...)
	out = append(out, chunk...)
	return append(out, b[ihdrEnd:]...)
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// removeDesktopOverride borra el lanzador propio, solo si lo creó Hilvan.
var legacyRemoved bool

func removeDesktopOverride() error {
	legacyRemoved = false
	p := filepath.Join(dataHome(), "applications", desktopID())
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	if !bytes.Contains(b, []byte(desktopMarker)) {
		return nil
	}
	legacyRemoved = true
	return os.Remove(p)
}

// DecodeIcon lee un PNG de ícono (para la bandeja).
func DecodeIcon(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }
