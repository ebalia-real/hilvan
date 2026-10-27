package core

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// ErrCanceled: la persona cerró el diálogo sin elegir dónde guardar.
var ErrCanceled = errors.New("cancelado")

// saveTarget describe qué se guarda de un mensaje: su archivo o su texto.
type saveTarget struct {
	name string
	file bool
	text string
}

func (a *Account) saveTargetOf(ctx context.Context, roomID, eventID string) (saveTarget, error) {
	c, err := a.eventContent(ctx, roomID, eventID)
	if err != nil {
		return saveTarget{}, err
	}
	if c.Get("url").Exists() || c.Get("file").Exists() || c.Get("io\\.hilvan\\.parts").Exists() {
		name := c.Get("filename").Str
		if name == "" {
			name = c.Get("body").Str
		}
		name = sanitizeFileName(name)
		if name == "" || name == "." {
			name = "archivo"
		}
		// "image" sin extensión: se la ponemos según el tipo, para que se abra bien.
		if filepath.Ext(name) == "" {
			if exts, _ := mime.ExtensionsByType(c.Get("info.mimetype").Str); len(exts) > 0 {
				name += preferredExt(exts)
			}
		}
		return saveTarget{name: name, file: true}, nil
	}
	body := c.Get("body").Str
	if body == "" {
		return saveTarget{}, errors.New("este mensaje no tiene nada para guardar")
	}
	when := time.Now()
	who := ""
	if row := a.store.GetEvent(eventID); row != nil {
		when = time.UnixMilli(row.TS)
		who = row.Sender
	}
	text := body
	if who != "" {
		text = fmt.Sprintf("%s — %s\n\n%s\n", who, when.Format("02/01/2006 15:04"), body)
	}
	return saveTarget{name: "mensaje-" + when.Format("2006-01-02-1504") + ".txt", text: text}, nil
}

func preferredExt(exts []string) string {
	for _, e := range exts {
		switch e {
		case ".jpg", ".png", ".mp4", ".webm", ".gif", ".webp", ".mp3", ".ogg", ".pdf", ".txt":
			return e
		}
	}
	return exts[0]
}

// SaveAs pregunta dónde guardar (con el selector de archivos del sistema) y guarda. Devuelve la
// ruta elegida; el archivo se baja y verifica con save (puede tardar si es grande).
func (a *Account) SaveAs(ctx context.Context, roomID, eventID string) (dest string, save func() error, err error) {
	tg, err := a.saveTargetOf(ctx, roomID, eventID)
	if err != nil {
		return "", nil, err
	}
	folder := a.m.Settings().LastSaveDir
	if st, err := os.Stat(folder); folder == "" || err != nil || !st.IsDir() {
		folder = downloadsDir()
	}
	pick := a.m.PickSavePath
	if pick == nil {
		pick = portalSavePath
	}
	dest, err = pick("Guardar como", tg.name, folder)
	if err != nil {
		return "", nil, err
	}
	if dest == "" {
		return "", nil, ErrCanceled
	}
	if a.m.ExportFile != nil && strings.Contains(dest, "://") {
		// Android: el diálogo devuelve una URI; se guarda en un temporal y el sistema lo copia.
		return dest, func() error {
			tmp, err := os.CreateTemp("", "hilvan-guardar-*")
			if err != nil {
				return err
			}
			tmp.Close()
			defer os.Remove(tmp.Name())
			if tg.file {
				if err := a.saveFileTo(a.ctx, roomID, eventID, tmp.Name()); err != nil {
					return err
				}
			} else if err := os.WriteFile(tmp.Name(), []byte(tg.text), 0o600); err != nil {
				return err
			}
			_, err = a.m.ExportFile(tmp.Name(), dest, tg.name, mime.TypeByExtension(filepath.Ext(tg.name)))
			return err
		}, nil
	}
	if dir := filepath.Dir(dest); dir != folder {
		b := fmt.Appendf(nil, `{"last_save_dir":%q}`, dir)
		_, _ = a.m.settings.Patch(b)
	}
	if !tg.file {
		return dest, func() error { return writeFileAtomic(dest, []byte(tg.text)) }, nil
	}
	return dest, func() error { return a.saveFileTo(a.ctx, roomID, eventID, dest) }, nil
}

// saveFileTo baja, verifica y copia el archivo de un evento a dest (reemplaza si existe: el
// diálogo ya preguntó).
func (a *Account) saveFileTo(ctx context.Context, roomID, eventID, dest string) error {
	res, err := a.FileOf(ctx, roomID, eventID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".hilvan-part"
	if err := copyFile(res.Path, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// portalSavePath usa el selector de archivos del portal (sin ventana propia, por ejemplo en
// modo navegador).
func portalSavePath(title, name, folder string) (string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", errors.New("no hay selector de archivos disponible")
	}
	defer conn.Close()
	opts := map[string]dbus.Variant{
		"modal":        dbus.MakeVariant(true),
		"current_name": dbus.MakeVariant(name),
	}
	if folder != "" {
		opts["current_folder"] = dbus.MakeVariant(append([]byte(folder), 0))
	}
	res, err := portalCall(conn, "org.freedesktop.portal.FileChooser", "SaveFile", opts, "", title)
	if err != nil {
		if err.Error() == "cancelado" {
			return "", nil
		}
		return "", err
	}
	var uris []string
	if v, ok := res["uris"]; ok {
		_ = v.Store(&uris)
	}
	if len(uris) == 0 {
		return "", nil
	}
	u, err := url.Parse(uris[0])
	if err != nil || u.Scheme != "file" {
		return "", errors.New("ubicación no válida")
	}
	return u.Path, nil
}

// SaveText guarda un texto (claves exportadas, clave de recuperación) donde elija la persona.
// Sirve donde la interfaz no puede descargar archivos (Android).
func (m *Manager) SaveText(name, text string) (string, error) {
	pick := m.PickSavePath
	if pick == nil {
		pick = portalSavePath
	}
	folder := m.Settings().LastSaveDir
	if folder == "" {
		folder = downloadsDir()
	}
	dest, err := pick("Guardar como", sanitizeFileName(name), folder)
	if err != nil {
		return "", err
	}
	if dest == "" {
		return "", ErrCanceled
	}
	if m.ExportFile != nil && strings.Contains(dest, "://") {
		tmp, err := os.CreateTemp("", "hilvan-texto-*")
		if err != nil {
			return "", err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.WriteString(text); err != nil {
			tmp.Close()
			return "", err
		}
		tmp.Close()
		return m.ExportFile(tmp.Name(), dest, name, "text/plain")
	}
	return dest, writeFileAtomic(dest, []byte(text))
}
