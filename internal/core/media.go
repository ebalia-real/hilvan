package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"golang.org/x/image/draw"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var mediaFetchLocks = newKeyedMutex()

type MediaResult struct {
	Path string
	Data []byte
	Mime string
	Name string
}

func (m *Manager) mediaCacheDir() string { return filepath.Join(m.Paths.Cache, "media") }

func cacheKey(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:16])
}

// FetchMedia devuelve un archivo mxc (o su miniatura) desde la caché o el servidor.
func (a *Account) FetchMedia(ctx context.Context, mxc string, w, h int, method string, animated bool) (*MediaResult, error) {
	uri, err := id.ParseContentURI(mxc)
	if err != nil || uri.IsEmpty() {
		return nil, errors.New("mxc inválido")
	}
	key := cacheKey(a.UserID(), mxc, fmt.Sprint(w, h, method, animated))
	dir := filepath.Join(a.m.mediaCacheDir(), key[:2])
	path := filepath.Join(dir, key)
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return &MediaResult{Path: path, Mime: readMime(path)}, nil
	}
	unlock := mediaFetchLocks.Lock(key)
	defer unlock()
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return &MediaResult{Path: path, Mime: readMime(path)}, nil
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	var mime string
	if w > 0 && h > 0 {
		resp, terr := a.cli.DownloadThumbnail(ctx, uri, h, w, mautrix.DownloadThumbnailExtra{Method: method, Animated: animated})
		if terr == nil {
			defer resp.Body.Close()
			f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return nil, err
			}
			// Una miniatura nunca es enorme; si lo fuera, error (no un archivo truncado en silencio).
			n, err := io.Copy(f, io.LimitReader(resp.Body, 64<<20+1))
			f.Close()
			if err == nil && n > 64<<20 {
				err = errors.New("miniatura demasiado grande")
			}
			if err == nil && resp.ContentLength > 0 && n != resp.ContentLength {
				err = fmt.Errorf("miniatura incompleta (%d de %d bytes)", n, resp.ContentLength)
			}
			if err != nil {
				os.Remove(tmp)
				return nil, err
			}
			mime = resp.Header.Get("Content-Type")
		}
	}
	if mime == "" {
		// Original (o el servidor no genera miniaturas): descarga reanudable y sin tope de tamaño.
		if w == 0 && h == 0 {
			if err := a.scanFile(ctx, fileRef{URL: mxc}); err != nil {
				return nil, err
			}
		}
		os.Remove(tmp)
		if _, err := a.downloadRaw(ctx, mxc, tmp, 0, nil); err != nil {
			os.Remove(tmp)
			return nil, err
		}
		if b, err := os.ReadFile(tmp + ".mime"); err == nil {
			mime = string(b)
		}
		if mime == "" {
			if f, err := os.Open(tmp); err == nil {
				head := make([]byte, 512)
				n, _ := io.ReadFull(f, head)
				f.Close()
				mime = http.DetectContentType(head[:n])
			}
		}
	}
	if err = os.Rename(tmp, path); err != nil {
		return nil, err
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	_ = os.WriteFile(path+".mime", []byte(mime), 0o600)
	return &MediaResult{Path: path, Mime: mime}, nil
}

func readMime(path string) string {
	b, err := os.ReadFile(path + ".mime")
	if err != nil {
		return "application/octet-stream"
	}
	return string(b)
}

// decryptedCache guarda en memoria (no en disco) archivos descifrados recientes.
type memCache struct {
	mu    sync.Mutex
	items map[string]*memItem
	size  int
	max   int
}

type memItem struct {
	data []byte
	mime string
	name string
	used time.Time
}

var decrypted = &memCache{items: map[string]*memItem{}, max: 192 << 20}

func (c *memCache) get(k string) *memItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	if it, ok := c.items[k]; ok {
		it.used = time.Now()
		return it
	}
	return nil
}

func (c *memCache) put(k string, it *memItem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(it.data) > c.max/4 {
		return
	}
	it.used = time.Now()
	c.items[k] = it
	c.size += len(it.data)
	for c.size > c.max && len(c.items) > 0 {
		var oldK string
		var oldT time.Time
		for kk, v := range c.items {
			if oldK == "" || v.used.Before(oldT) {
				oldK, oldT = kk, v.used
			}
		}
		c.size -= len(c.items[oldK].data)
		delete(c.items, oldK)
	}
}

// FetchEncryptedMedia descifra el adjunto (o miniatura) de un evento cifrado.
func (a *Account) FetchEncryptedMedia(ctx context.Context, roomID, eventID string, thumb bool) (*MediaResult, error) {
	k := a.UserID() + eventID + fmt.Sprint(thumb)
	if it := decrypted.get(k); it != nil {
		return &MediaResult{Data: it.data, Mime: it.mime, Name: it.name}, nil
	}
	row := a.store.GetEvent(eventID)
	var content gjson.Result
	if row != nil {
		content = gjson.ParseBytes(row.Content)
		// Si fue editado (p. ej. cambio de imagen), usar la última versión.
		for _, rel := range a.store.Relations([]string{eventID}) {
			if rel.RelType == "m.replace" && rel.Sender == row.Sender {
				if nc := gjson.GetBytes(rel.Content, `m\.new_content`); nc.Exists() && nc.Get("file").Exists() {
					content = nc
				}
			}
		}
	} else {
		item, _, err := a.FetchEvent(ctx, roomID, eventID)
		if err != nil {
			return nil, err
		}
		content = gjson.ParseBytes(item.Content)
	}
	// Archivos grandes (o divididos en partes): a disco, verificados y servibles por rangos (videos largos).
	if !thumb && (content.Get("info.size").Int() > 32<<20 || content.Get("msgtype").Str == msgtypeMultipart) {
		return a.FileOf(ctx, roomID, eventID)
	}
	fileJSON := content.Get("file")
	mime := content.Get("info.mimetype").Str
	if thumb {
		if tf := content.Get("info.thumbnail_file"); tf.Exists() {
			fileJSON = tf
			mime = content.Get("info.thumbnail_info.mimetype").Str
		}
	}
	if !fileJSON.Exists() {
		return nil, errors.New("el evento no tiene archivo cifrado")
	}
	var info event.EncryptedFileInfo
	if err := json.Unmarshal([]byte(fileJSON.Raw), &info); err != nil {
		return nil, err
	}
	if !thumb {
		if err := a.scanFile(ctx, fileRef{File: &info}); err != nil {
			return nil, err
		}
	}
	uri, err := info.URL.Parse()
	if err != nil {
		return nil, err
	}
	res, err := a.FetchMedia(ctx, uri.String(), 0, 0, "", false)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		return nil, err
	}
	if err = info.DecryptInPlace(data); err != nil {
		return nil, fmt.Errorf("no se pudo descifrar el archivo: %w", err)
	}
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	name := content.Get("filename").Str
	if name == "" {
		name = content.Get("body").Str
	}
	decrypted.put(k, &memItem{data: data, mime: safeMime(mime), name: name})
	return &MediaResult{Data: data, Mime: safeMime(mime), Name: name}, nil
}

// safeMime evita servir tipos que el navegador podría ejecutar.
func safeMime(m string) string {
	m = strings.ToLower(strings.TrimSpace(strings.Split(m, ";")[0]))
	switch {
	case strings.HasPrefix(m, "image/") && m != "image/svg+xml":
		return m
	case strings.HasPrefix(m, "video/"), strings.HasPrefix(m, "audio/"):
		return m
	case m == "application/pdf":
		return m
	}
	return "application/octet-stream"
}

// CleanMediaCache recorta la caché de disco (más vieja primero) a maxBytes.
func (m *Manager) CleanMediaCache(maxBytes int64) {
	type f struct {
		path string
		size int64
		at   time.Time
	}
	var files []f
	var total int64
	walk := func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasSuffix(p, ".mime") {
			return nil
		}
		// Restos de transferencias cortadas (más de un día) y subidas viejas: fuera.
		if (strings.HasSuffix(p, ".part") || strings.Contains(filepath.Base(p), ".raw") || strings.HasPrefix(filepath.Base(p), "up-")) &&
			time.Since(info.ModTime()) > 24*time.Hour {
			_ = os.Remove(p)
			return nil
		}
		files = append(files, f{p, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	}
	_ = filepath.Walk(m.mediaCacheDir(), walk)
	_ = filepath.Walk(filepath.Join(m.Paths.Cache, "files"), walk)   // archivos grandes ya verificados
	_ = filepath.Walk(filepath.Join(m.Paths.Cache, "uploads"), walk) // temporales de envío
	if total <= maxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.Before(files[j].at) })
	for _, fl := range files {
		if total <= maxBytes {
			break
		}
		_ = os.Remove(fl.path)
		_ = os.Remove(fl.path + ".mime")
		total -= fl.size
	}
}

// --- blurhash (codificador compacto) ---

const b83chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz#$%*+,-.:;=?@[]^_{|}~"

func b83(v, length int) string {
	out := make([]byte, length)
	for i := 1; i <= length; i++ {
		digit := (v / int(math.Pow(83, float64(length-i)))) % 83
		out[i-1] = b83chars[digit]
	}
	return string(out)
}

func srgbToLinear(v uint8) float64 {
	c := float64(v) / 255
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func linearToSrgb(v float64) int {
	v = math.Max(0, math.Min(1, v))
	if v <= 0.0031308 {
		return int(v*12.92*255 + 0.5)
	}
	return int((1.055*math.Pow(v, 1/2.4)-0.055)*255 + 0.5)
}

func signPow(v, e float64) float64 { return math.Copysign(math.Pow(math.Abs(v), e), v) }

func blurhashOf(data []byte) string {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	// Reducimos a 32px para que sea rápido.
	b := img.Bounds()
	sw, sh := 32, 32
	if b.Dx() > b.Dy() {
		sh = max(1, 32*b.Dy()/b.Dx())
	} else {
		sw = max(1, 32*b.Dx()/b.Dy())
	}
	small := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), img, b, draw.Src, nil)
	cx, cy := 4, 3
	if sh > sw {
		cx, cy = 3, 4
	}
	factors := make([][3]float64, 0, cx*cy)
	for y := 0; y < cy; y++ {
		for x := 0; x < cx; x++ {
			norm := 2.0
			if x == 0 && y == 0 {
				norm = 1
			}
			var r, g, bl float64
			for py := 0; py < sh; py++ {
				for px := 0; px < sw; px++ {
					basis := norm * math.Cos(math.Pi*float64(x)*float64(px)/float64(sw)) * math.Cos(math.Pi*float64(y)*float64(py)/float64(sh))
					o := small.PixOffset(px, py)
					r += basis * srgbToLinear(small.Pix[o])
					g += basis * srgbToLinear(small.Pix[o+1])
					bl += basis * srgbToLinear(small.Pix[o+2])
				}
			}
			scale := 1 / float64(sw*sh)
			factors = append(factors, [3]float64{r * scale, g * scale, bl * scale})
		}
	}
	dc := factors[0]
	ac := factors[1:]
	out := b83((cx-1)+(cy-1)*9, 1)
	maxVal := 1.0
	if len(ac) > 0 {
		actual := 0.0
		for _, f := range ac {
			actual = math.Max(actual, math.Max(math.Abs(f[0]), math.Max(math.Abs(f[1]), math.Abs(f[2]))))
		}
		q := int(math.Max(0, math.Min(82, math.Floor(actual*166-0.5))))
		maxVal = float64(q+1) / 166
		out += b83(q, 1)
	} else {
		out += b83(0, 1)
	}
	out += b83((linearToSrgb(dc[0])<<16)+(linearToSrgb(dc[1])<<8)+linearToSrgb(dc[2]), 4)
	for _, f := range ac {
		q := func(v float64) int {
			return int(math.Max(0, math.Min(18, math.Floor(signPow(v/maxVal, 0.5)*9+9.5))))
		}
		out += b83(q(f[0])*19*19+q(f[1])*19+q(f[2]), 2)
	}
	return out
}

// UserProfile obtiene el perfil global de un usuario.
func (a *Account) UserProfile(ctx context.Context, userID string) (map[string]any, error) {
	p, err := a.cli.GetProfile(ctx, id.UserID(userID))
	if err != nil {
		return nil, friendlyError(err)
	}
	out := map[string]any{"user_id": userID, "displayname": p.DisplayName, "avatar_url": p.AvatarURL.String()}
	a.presenceMu.RLock()
	if pr, ok := a.presence[userID]; ok {
		out["presence"] = pr
	}
	a.presenceMu.RUnlock()
	return out, nil
}
