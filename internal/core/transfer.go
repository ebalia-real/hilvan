package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Archivos grandes sin límites ni corrupción:
//   - todo pasa por disco en streaming (nunca el archivo entero en memoria);
//   - el archivo se sube entero, como un archivo Matrix normal que cualquier cliente abre (sin dividirlo);
//     si supera el tope del servidor, se avisa antes de subir nada;
//   - las descargas se reanudan con Range si se corta la conexión y se verifican con SHA-256
//     antes de entregar el archivo: un archivo dañado nunca aparece como si estuviera bien;
//   - si la conexión se queda colgada, se detecta y se reintenta.

const (
	msgtypeMultipart = "io.hilvan.multipart"
	keyParts         = "io.hilvan.parts"
	keySHA256        = "io.hilvan.sha256"
	stallTimeout     = 90 * time.Second
	transferAttempts = 8
)

// Transfer es el progreso de una subida o descarga, para la interfaz.
type Transfer struct {
	ID      string `json:"id"` // txn (subidas) o id del evento (descargas)
	Room    string `json:"room"`
	Name    string `json:"name"`
	Dir     string `json:"dir"` // up | down
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Part    int    `json:"part,omitempty"`
	Parts   int    `json:"parts,omitempty"`
	State   string `json:"state"` // running | verifying | done | error
	Error   string `json:"error,omitempty"`
	last    time.Time
	account string
}

func (a *Account) pushTransfer(t *Transfer, force bool) {
	if !force && time.Since(t.last) < 250*time.Millisecond {
		return
	}
	t.last = time.Now()
	cp := *t
	a.m.Broadcast(Push{Ev: "transfer", A: a.UserID(), D: &cp})
}

// --- lector con progreso y detección de conexiones colgadas ---

type progressReader struct {
	r       io.Reader
	n       int64
	onRead  func(n int64)
	touched chan struct{}
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.n += int64(n)
		if p.onRead != nil {
			p.onRead(p.n)
		}
		select {
		case p.touched <- struct{}{}:
		default:
		}
	}
	return n, err
}

// watchStall cancela la operación si no se mueve ni un byte durante stallTimeout.
func watchStall(ctx context.Context, touched chan struct{}) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		t := time.NewTimer(stallTimeout)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-touched:
				if !t.Stop() {
					<-t.C
				}
				t.Reset(stallTimeout)
			case <-t.C:
				cancel(errors.New("la conexión se quedó sin avanzar"))
				return
			}
		}
	}()
	return ctx, func() { close(done); cancel(nil) }
}

// --- límite de subida del servidor ---

type cachedLimit struct {
	limit int64
	at    time.Time
}

var uploadLimits sync.Map // homeserver → cachedLimit

// uploadLimit es el tope por archivo que anuncia el servidor (0 = desconocido). Se renueva cada 10 min.
func (a *Account) uploadLimit(ctx context.Context) int64 {
	key := a.cli.HomeserverURL.String()
	if v, ok := uploadLimits.Load(key); ok && time.Since(v.(cachedLimit).at) < 10*time.Minute {
		return v.(cachedLimit).limit
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var limit int64
	if cfg, err := a.cli.GetMediaConfig(cctx); err == nil && cfg.UploadSize > 0 {
		limit = cfg.UploadSize
	}
	uploadLimits.Store(key, cachedLimit{limit, time.Now()})
	return limit
}

func (a *Account) forgetUploadLimit() { uploadLimits.Delete(a.cli.HomeserverURL.String()) }

// ErrTooBig explica que el tope es del servidor (no de Hilvan) y cómo subirlo.
func errTooBig(size, limit int64) error {
	if limit <= 0 {
		return fmt.Errorf("el servidor rechazó el archivo (%s) por ser demasiado grande. Es un límite del servidor, no de Hilvan: "+
			"quien lo administra puede subirlo (max_upload_size en Synapse y el tamaño máximo del proxy, p. ej. client_max_body_size en nginx)", humanSize(size))
	}
	return fmt.Errorf("el archivo pesa %s y tu servidor acepta hasta %s por archivo. Es un límite del servidor, no de Hilvan: "+
		"quien lo administra puede subirlo (max_upload_size en Synapse y el tamaño máximo del proxy, p. ej. client_max_body_size en nginx)",
		humanSize(size), humanSize(limit))
}

// CheckUploadSize avisa antes de subir si el archivo no entra en el servidor.
func (a *Account) CheckUploadSize(ctx context.Context, size int64) error {
	if limit := a.uploadLimit(ctx); limit > 0 && size > limit {
		return errTooBig(size, limit)
	}
	return nil
}

// --- subida en streaming ---

type uploadedPart struct {
	URL    string          `json:"url,omitempty"`
	File   json.RawMessage `json:"file,omitempty"`
	Size   int64           `json:"size"`
	SHA256 string          `json:"sha256"` // del contenido original (base64 sin relleno)
}

// uploadStream sube length bytes de src (que se puede releer con open) cifrándolos si hace falta.
func (a *Account) uploadStream(ctx context.Context, open func() (io.ReadCloser, error), length int64, mime, name string,
	encrypted bool, onProgress func(int64)) (*uploadedPart, error) {
	var lastErr error
	for attempt := 0; attempt < transferAttempts; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<min(attempt, 5)) * time.Second
			a.log.Warn().Err(lastErr).Dur("retry_in", wait).Msg("Reintentando la subida")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		part, err := a.uploadOnce(ctx, open, length, mime, name, encrypted, onProgress)
		if err == nil {
			return part, nil
		}
		lastErr = err
		if ctx.Err() != nil || isPermanentUploadError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func isPermanentUploadError(err error) bool {
	var he mautrix.HTTPError
	if errors.As(err, &he) && he.Response != nil {
		switch he.Response.StatusCode {
		case http.StatusRequestEntityTooLarge, http.StatusUnauthorized, http.StatusForbidden:
			return true
		}
	}
	return errors.Is(err, mautrix.MTooLarge) || errors.Is(err, mautrix.MForbidden)
}

func (a *Account) uploadOnce(ctx context.Context, open func() (io.ReadCloser, error), length int64, mime, name string,
	encrypted bool, onProgress func(int64)) (*uploadedPart, error) {
	src, err := open()
	if err != nil {
		return nil, err
	}
	plainHash := sha256.New()
	var body io.Reader = io.TeeReader(src, plainHash)
	var ef *attachment.EncryptedFile
	var enc io.ReadSeekCloser
	if encrypted {
		ef = attachment.NewEncryptedFile()
		enc = ef.EncryptStream(body)
		body = enc
	}
	touched := make(chan struct{}, 1)
	pr := &progressReader{r: body, onRead: onProgress, touched: touched}
	sctx, stop := watchStall(ctx, touched)
	defer stop()
	ct, fname := mime, name
	if encrypted {
		ct, fname = "application/octet-stream", ""
	}
	u, _ := url.Parse(a.cli.BuildURL(mautrix.MediaURLPath{"v3", "upload"}))
	if fname != "" {
		q := u.Query()
		q.Set("filename", fname)
		u.RawQuery = q.Encode()
	}
	var resp mautrix.RespMediaUpload
	_, err = a.cli.MakeFullRequest(sctx, mautrix.FullRequest{
		Method: http.MethodPost, URL: u.String(), Headers: http.Header{"Content-Type": {ct}},
		RequestBody: io.NopCloser(pr), RequestLength: length, ResponseJSON: &resp, MaxAttempts: 1,
	})
	if enc != nil {
		_ = enc.Close() // calcula el hash del cifrado (y cierra el archivo)
	} else {
		_ = src.Close()
	}
	if err != nil {
		if c := context.Cause(sctx); c != nil && ctx.Err() == nil {
			return nil, c
		}
		return nil, friendlyError(err)
	}
	if pr.n != length {
		return nil, fmt.Errorf("se leyeron %d de %d bytes del archivo", pr.n, length)
	}
	part := &uploadedPart{Size: length, SHA256: base64.RawStdEncoding.EncodeToString(plainHash.Sum(nil))}
	if encrypted {
		info := event.EncryptedFileInfo{EncryptedFile: *ef, URL: resp.ContentURI.CUString()}
		part.File, _ = json.Marshal(info)
	} else {
		part.URL = resp.ContentURI.String()
	}
	return part, nil
}

// uploadFile sube un archivo del disco entero, en streaming, con reintentos y progreso.
func (a *Account) uploadFile(ctx context.Context, path string, size int64, mime, name string, encrypted bool,
	t *Transfer) (*uploadedPart, error) {
	if err := a.CheckUploadSize(ctx, size); err != nil {
		return nil, err
	}
	open := func() (io.ReadCloser, error) { return os.Open(path) }
	part, err := a.uploadStream(ctx, open, size, mime, name, encrypted, func(done int64) {
		t.Done = done
		a.pushTransfer(t, false)
	})
	if err != nil && isTooLarge(err) {
		a.forgetUploadLimit() // el tope pudo cambiar: la próxima vez se vuelve a preguntar
		return nil, errTooBig(size, a.uploadLimit(ctx))
	}
	return part, err
}

func isTooLarge(err error) bool {
	var he mautrix.HTTPError
	if errors.As(err, &he) && he.Response != nil && he.Response.StatusCode == http.StatusRequestEntityTooLarge {
		return true
	}
	return errors.Is(err, mautrix.MTooLarge) || strings.Contains(err.Error(), "M_TOO_LARGE")
}

// --- descarga en streaming, reanudable y verificada ---

// downloadRaw baja un mxc a dest (tal cual, cifrado o no), reanudando con Range si se corta.
func (a *Account) downloadRaw(ctx context.Context, mxc string, dest string, expect int64, onProgress func(int64)) (int64, error) {
	uri, err := id.ParseContentURI(mxc)
	if err != nil || uri.IsEmpty() {
		return 0, errors.New("mxc inválido")
	}
	var lastErr error
	for attempt := 0; attempt < transferAttempts; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<min(attempt, 5)) * time.Second
			a.log.Warn().Err(lastErr).Dur("retry_in", wait).Msg("Reintentando la descarga")
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(wait):
			}
		}
		n, done, err := a.downloadOnce(ctx, uri, dest, expect, onProgress)
		if done {
			return n, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		var he mautrix.HTTPError
		if errors.As(err, &he) && he.Response != nil && (he.Response.StatusCode == 404 || he.Response.StatusCode == 403) {
			return 0, err
		}
	}
	return 0, lastErr
}

func (a *Account) downloadOnce(ctx context.Context, uri id.ContentURI, dest string, expect int64, onProgress func(int64)) (int64, bool, error) {
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	have, _ := f.Seek(0, io.SeekEnd)
	if expect > 0 && have == expect {
		return have, true, nil
	}
	if expect > 0 && have > expect {
		_ = f.Truncate(0)
		have, _ = f.Seek(0, io.SeekStart)
	}
	touched := make(chan struct{}, 1)
	sctx, stop := watchStall(ctx, touched)
	defer stop()
	u := a.cli.BuildURL(mautrix.ClientURLPath{"v1", "media", "download", uri.Homeserver, uri.FileID})
	req, err := http.NewRequestWithContext(sctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+a.cli.AccessToken)
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := a.cli.Client.Do(req)
	if err != nil {
		if c := context.Cause(sctx); c != nil && ctx.Err() == nil {
			return have, false, c
		}
		return have, false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		// sigue desde "have"
	case resp.StatusCode == http.StatusOK:
		if have > 0 { // el servidor no soporta Range: empezamos de nuevo
			_ = f.Truncate(0)
			have, _ = f.Seek(0, io.SeekStart)
		}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && expect > 0 && have >= expect:
		return have, true, nil
	default:
		return have, false, mautrix.HTTPError{Request: req, Response: resp, Message: "descarga fallida"}
	}
	total := expect
	if total <= 0 {
		if cl := resp.ContentLength; cl > 0 {
			total = have + cl
		}
	}
	pr := &progressReader{r: resp.Body, touched: touched, onRead: func(n int64) {
		if onProgress != nil {
			onProgress(have + n)
		}
	}}
	n, err := io.Copy(f, pr)
	have += n
	if err != nil {
		if c := context.Cause(sctx); c != nil && ctx.Err() == nil {
			err = c
		}
		return have, false, err
	}
	if total > 0 && have != total {
		return have, false, fmt.Errorf("descarga incompleta: %d de %d bytes", have, total)
	}
	if err := f.Sync(); err != nil {
		return have, false, err
	}
	return have, true, nil
}

func fileSHA256(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// fileRef describe un archivo (o una parte) de un evento.
type fileRef struct {
	URL    string
	File   *event.EncryptedFileInfo
	Size   int64
	SHA256 string // del contenido original, si se conoce
}

// fileRefs devuelve las partes del archivo de un evento (una sola si no fue dividido).
func fileRefs(content gjson.Result) ([]fileRef, error) {
	var refs []fileRef
	if parts := content.Get(strings.ReplaceAll(keyParts, ".", `\.`)); parts.IsArray() {
		for _, p := range parts.Array() {
			r := fileRef{URL: p.Get("url").Str, Size: p.Get("size").Int(), SHA256: p.Get("sha256").Str}
			if f := p.Get("file"); f.Exists() {
				var info event.EncryptedFileInfo
				if err := json.Unmarshal([]byte(f.Raw), &info); err != nil {
					return nil, err
				}
				r.File = &info
			}
			refs = append(refs, r)
		}
		return refs, nil
	}
	r := fileRef{URL: content.Get("url").Str, Size: content.Get("info.size").Int()}
	if f := content.Get("file"); f.Exists() {
		var info event.EncryptedFileInfo
		if err := json.Unmarshal([]byte(f.Raw), &info); err != nil {
			return nil, err
		}
		r.File = &info
	}
	if r.URL == "" && r.File == nil {
		return nil, errors.New("el mensaje no tiene archivo")
	}
	return append(refs, r), nil
}

// eventContent devuelve el contenido vigente de un evento (con ediciones aplicadas).
func (a *Account) eventContent(ctx context.Context, roomID, eventID string) (gjson.Result, error) {
	row := a.store.GetEvent(eventID)
	if row == nil {
		item, _, err := a.FetchEvent(ctx, roomID, eventID)
		if err != nil {
			return gjson.Result{}, err
		}
		return gjson.ParseBytes(item.Content), nil
	}
	content := gjson.ParseBytes(row.Content)
	for _, rel := range a.store.Relations([]string{eventID}) {
		if rel.RelType == "m.replace" && rel.Sender == row.Sender {
			if nc := gjson.GetBytes(rel.Content, `m\.new_content`); nc.Exists() && (nc.Get("file").Exists() || nc.Get("url").Exists()) {
				content = nc
			}
		}
	}
	return content, nil
}

var fileLocks = newKeyedMutex()

// FileOf baja (si hace falta), descifra y verifica el archivo de un evento y devuelve su ruta en disco.
// Sirve para archivos de cualquier tamaño y para los divididos en partes.
func (a *Account) FileOf(ctx context.Context, roomID, eventID string) (*MediaResult, error) {
	content, err := a.eventContent(ctx, roomID, eventID)
	if err != nil {
		return nil, err
	}
	refs, err := fileRefs(content)
	if err != nil {
		return nil, err
	}
	name := content.Get("filename").Str
	if name == "" {
		name = content.Get("body").Str
	}
	mime := content.Get("info.mimetype").Str
	key := cacheKey(a.UserID(), "file", eventID, content.Get("file.url").Str+content.Get("url").Str)
	dir := filepath.Join(a.m.Paths.Cache, "files", key[:2])
	final := filepath.Join(dir, key)
	if st, err := os.Stat(final); err == nil && st.Size() >= 0 {
		now := time.Now()
		_ = os.Chtimes(final, now, now)
		return &MediaResult{Path: final, Mime: safeMime(mime), Name: name}, nil
	}
	unlock := fileLocks.Lock(key)
	defer unlock()
	if _, err := os.Stat(final); err == nil {
		return &MediaResult{Path: final, Mime: safeMime(mime), Name: name}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var total int64
	for _, r := range refs {
		total += r.Size
	}
	t := &Transfer{ID: eventID, Room: roomID, Name: name, Dir: "down", Total: total, Parts: len(refs), State: "running"}
	a.pushTransfer(t, true)
	fail := func(err error) (*MediaResult, error) {
		t.State, t.Error = "error", err.Error()
		a.pushTransfer(t, true)
		return nil, err
	}
	assembling := final + ".part"
	out, err := os.OpenFile(assembling, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fail(err)
	}
	var done int64
	whole := sha256.New()
	// Antivirus de la organización (si hay): antes de bajar nada.
	for _, r := range refs {
		if err := a.scanFile(ctx, r); err != nil {
			out.Close()
			os.Remove(assembling)
			return fail(err)
		}
	}
	for i, r := range refs {
		t.Part = i + 1
		raw := fmt.Sprintf("%s.raw%d", final, i)
		mxc := r.URL
		expect := r.Size
		if r.File != nil {
			mxc = string(r.File.URL)
			expect = 0 // el cifrado mide lo mismo, pero no todos los clientes publican el tamaño
		}
		base := done
		if _, err := a.downloadRaw(ctx, mxc, raw, expect, func(n int64) {
			t.Done = base + n
			a.pushTransfer(t, false)
		}); err != nil {
			out.Close()
			return fail(err)
		}
		t.State = "verifying"
		a.pushTransfer(t, true)
		n, err := copyVerified(out, raw, r, whole)
		os.Remove(raw)
		if err != nil {
			out.Close()
			os.Remove(assembling)
			return fail(err)
		}
		done += n
		t.State = "running"
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fail(err)
	}
	out.Close()
	if want := content.Get(strings.ReplaceAll(keySHA256, ".", `\.`)).Str; want != "" {
		if got := hex.EncodeToString(whole.Sum(nil)); !strings.EqualFold(got, want) {
			os.Remove(assembling)
			return fail(errors.New("el archivo llegó dañado (la verificación SHA-256 no coincide)"))
		}
	}
	if err := os.Rename(assembling, final); err != nil {
		return fail(err)
	}
	t.State, t.Done = "done", done
	a.pushTransfer(t, true)
	if mime == "" {
		mime = "application/octet-stream"
	}
	return &MediaResult{Path: final, Mime: safeMime(mime), Name: name}, nil
}

// copyVerified descifra (si hace falta) raw hacia out, verificando los hashes.
func copyVerified(out io.Writer, raw string, r fileRef, whole hash.Hash) (int64, error) {
	if r.File != nil {
		// Primero el hash del cifrado: si no coincide, ni se descifra.
		if err := r.File.PrepareForDecryption(); err != nil {
			return 0, err
		}
		sum, err := fileSHA256(raw)
		if err != nil {
			return 0, err
		}
		if got := base64.RawStdEncoding.EncodeToString(sum); got != strings.TrimRight(r.File.Hashes.SHA256, "=") {
			return 0, errors.New("el archivo llegó dañado (la verificación SHA-256 no coincide)")
		}
	}
	f, err := os.Open(raw)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var src io.Reader = f
	var dec io.ReadSeekCloser
	if r.File != nil {
		dec = r.File.DecryptStream(f)
		src = dec
	}
	ph := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, ph, whole), src)
	if err != nil {
		return n, err
	}
	if dec != nil {
		if err := dec.Close(); err != nil {
			return n, errors.New("el archivo llegó dañado (no pasa la verificación del cifrado)")
		}
	}
	if r.Size > 0 && n != r.Size && r.File == nil {
		return n, fmt.Errorf("tamaño inesperado: %d en vez de %d bytes", n, r.Size)
	}
	if r.SHA256 != "" {
		if got := base64.RawStdEncoding.EncodeToString(ph.Sum(nil)); got != strings.TrimRight(r.SHA256, "=") {
			return n, errors.New("una parte del archivo llegó dañada (la verificación SHA-256 no coincide)")
		}
	}
	return n, nil
}

// SaveFile guarda el archivo de un evento en una carpeta (Descargas), verificado y sin pisar otros.
func (a *Account) SaveFile(ctx context.Context, roomID, eventID, dir string) (string, error) {
	res, err := a.FileOf(ctx, roomID, eventID)
	if err != nil {
		return "", err
	}
	if dir == "" && a.m.ExportFile != nil {
		// Android: a la carpeta Descargas del sistema.
		return a.m.ExportFile(res.Path, "downloads:", sanitizeFileName(res.Name), res.Mime)
	}
	if dir == "" {
		dir = downloadsDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := sanitizeFileName(res.Name)
	dest := uniquePath(filepath.Join(dir, name))
	tmp := dest + ".hilvan-part"
	if err := copyFile(res.Path, tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dest, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func downloadsDir() string {
	if d := os.Getenv("HILVAN_DOWNLOAD_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_DOWNLOAD_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	for _, n := range []string{"Downloads", "Descargas"} {
		if st, err := os.Stat(filepath.Join(home, n)); err == nil && st.IsDir() {
			return filepath.Join(home, n)
		}
	}
	return filepath.Join(home, "Downloads")
}

func sanitizeFileName(n string) string {
	n = filepath.Base(strings.ReplaceAll(n, "\\", "/"))
	n = strings.Map(func(r rune) rune {
		if r < 32 || r == '/' || r == 0x7f {
			return '_'
		}
		return r
	}, n)
	if n == "" || n == "." || n == ".." {
		n = "archivo"
	}
	return n
}

func uniquePath(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		c := base + " (" + strconv.Itoa(i) + ")" + ext
		if _, err := os.Stat(c); os.IsNotExist(err) {
			return c
		}
	}
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strings.Replace(fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp]), ".", ",", 1)
}
