// Package server expone la interfaz web y el RPC del núcleo en 127.0.0.1.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog"

	"hilvan/internal/core"
)

type Server struct {
	m      *core.Manager
	log    zerolog.Logger
	assets fs.FS
	token  string
	ln     net.Listener
	srv    *http.Server
	URL    string
	Dev    bool // sirve assets desde disco (desarrollo)
	// Windows y Mac: otra copia de Hilvan que se abre le pide a esta que se muestre (y le pasa un
	// enlace matrix:) con esta clave, que solo está en un archivo privado del usuario.
	PresentKey string
	OnPresent  func(uri string)
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func New(m *core.Manager, assets fs.FS, port int, log zerolog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	s := &Server{m: m, log: log, assets: assets, token: randomToken(), ln: ln}
	s.URL = "http://" + ln.Addr().String()
	m.LocalURL = s.URL
	mux := http.NewServeMux()
	mux.HandleFunc("/callback/sso", s.handleSSOCallback)
	mux.HandleFunc("/callback/oauth", s.handleOAuthCallback)
	mux.HandleFunc("/ws", s.auth(s.handleWS))
	mux.HandleFunc("/appicon", s.auth(s.handleAppIcon))
	mux.HandleFunc("/media/", s.auth(s.handleMedia))
	mux.HandleFunc("/media-enc/", s.auth(s.handleEncMedia))
	mux.HandleFunc("/upload", s.auth(s.handleUpload))
	mux.HandleFunc("/file/", s.auth(s.handleFile))
	mux.HandleFunc("/download-keys", s.auth(s.handleExportKeys))
	mux.HandleFunc("/present", s.handlePresent)
	mux.HandleFunc("/", s.handleStatic)
	s.srv = &http.Server{Handler: s.guard(mux), ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

// LaunchURL incluye el token de un solo uso para el primer acceso.
func (s *Server) LaunchURL() string { return s.URL + "/?token=" + url.QueryEscape(s.token) }

func (s *Server) Serve() error {
	err := s.srv.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

// guard bloquea DNS rebinding y agrega cabeceras de seguridad.
func (s *Server) guard(next http.Handler) http.Handler {
	host := strings.TrimPrefix(s.URL, "http://")
	_, port, _ := net.SplitHostPort(host)
	allowed := map[string]bool{host: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "host no permitido", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(self), microphone=(self), display-capture=(self), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validCookie(r *http.Request) bool {
	c, err := r.Cookie("hv")
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validCookie(r) {
			http.Error(w, "no autorizado", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"media-src 'self' blob: data:; font-src 'self' data:; connect-src 'self' ws://127.0.0.1:* ws://localhost:*; " +
	"frame-src https:; worker-src 'self' blob:; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if tok := r.URL.Query().Get("token"); tok != "" {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1 {
			http.SetCookie(w, &http.Cookie{Name: "hv", Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		http.Error(w, "token inválido", http.StatusForbidden)
		return
	}
	if !s.validCookie(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Hilvan</title><p style="font-family:sans-serif">Abrí Hilvan desde su aplicación.</p>`)
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	data, err := fs.ReadFile(s.assets, p)
	if err != nil {
		// Rutas de la SPA: devolvemos index.html.
		if !strings.Contains(path.Base(p), ".") {
			p = "index.html"
			data, err = fs.ReadFile(s.assets, p)
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	ct := mime.TypeByExtension(path.Ext(p))
	if ct == "" {
		ct = "application/octet-stream"
	}
	if strings.HasSuffix(p, ".js") {
		ct = "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	if p == "index.html" {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("Cache-Control", "no-store")
	} else if s.Dev {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=300")
	}
	_, _ = w.Write(data)
}

// --- WebSocket RPC ---

type rpcReq struct {
	ID     int64           `json:"id"`
	Method string          `json:"m"`
	Params json.RawMessage `json:"p"`
}

type rpcErr struct {
	Code string `json:"code,omitempty"`
	Msg  string `json:"msg"`
}

type rpcResp struct {
	ID     int64   `json:"id"`
	Result any     `json:"r,omitempty"`
	Error  *rpcErr `json:"e,omitempty"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  []string{strings.TrimPrefix(s.URL, "http://")},
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(64 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	subID, ch := s.m.Subscribe()
	defer s.m.Unsubscribe(subID)

	var wmu sync.Mutex
	write := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		wmu.Lock()
		defer wmu.Unlock()
		wctx, c := context.WithTimeout(ctx, 30*time.Second)
		defer c()
		return conn.Write(wctx, websocket.MessageText, b)
	}

	go func() {
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case p, ok := <-ch:
				if !ok {
					cancel()
					return
				}
				if err := write(p); err != nil {
					cancel()
					return
				}
			case <-ping.C:
				pctx, c := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pctx)
				c()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		var req rpcReq
		if err := json.Unmarshal(data, &req); err != nil {
			continue
		}
		go func(req rpcReq) {
			defer func() {
				if rec := recover(); rec != nil {
					s.log.Error().Interface("panic", rec).Str("method", req.Method).Msg("Pánico en RPC")
					_ = write(rpcResp{ID: req.ID, Error: &rpcErr{Msg: "error interno"}})
				}
			}()
			cctx, c := context.WithTimeout(ctx, 5*time.Minute)
			defer c()
			res, err := s.m.Call(cctx, req.Method, req.Params)
			resp := rpcResp{ID: req.ID, Result: res}
			if err != nil {
				msg := err.Error()
				code := ""
				switch msg {
				case "password_required", "approval_required":
					code = msg
				}
				resp.Error = &rpcErr{Code: code, Msg: msg}
				resp.Result = nil
				s.log.Debug().Err(err).Str("method", req.Method).Msg("RPC con error")
			} else if res == nil {
				resp.Result = map[string]bool{"ok": true}
			}
			_ = write(resp)
		}(req)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// --- multimedia ---

// /media/{cuenta}/{servidor}/{id}?w=&h=&m=crop&anim=1
func (s *Server) handlePresent(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-Hilvan-Key")
	if r.Method != http.MethodPost || s.PresentKey == "" || s.OnPresent == nil ||
		subtle.ConstantTimeCompare([]byte(key), []byte(s.PresentKey)) != 1 {
		http.Error(w, "no autorizado", http.StatusUnauthorized)
		return
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	s.OnPresent(string(b))
	w.WriteHeader(http.StatusNoContent)
}

// handleAppIcon sirve el ícono elegido (o el clásico) para mostrarlo dentro de la app.
func (s *Server) handleAppIcon(w http.ResponseWriter, r *http.Request) {
	p := s.m.AppIconPath()
	if p == "" {
		http.Redirect(w, r, "/img/logo.svg", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, p)
}

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/media/"), "/", 3)
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	acctID, _ := url.PathUnescape(parts[0])
	a, err := s.m.Account(acctID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	wd, _ := strconv.Atoi(q.Get("w"))
	ht, _ := strconv.Atoi(q.Get("h"))
	method := q.Get("m")
	if method == "" {
		method = "scale"
	}
	res, err := a.FetchMedia(r.Context(), "mxc://"+parts[1]+"/"+parts[2], wd, ht, method, q.Get("anim") == "1")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	mt := res.Mime
	if q.Get("dl") == "1" {
		w.Header().Set("Content-Disposition", "attachment")
	}
	serveFile(w, r, res.Path, mt)
}

func serveFile(w http.ResponseWriter, r *http.Request, p, mt string) {
	mt = sanitizeMime(mt)
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'")
	http.ServeFile(w, r, p)
}

func sanitizeMime(mt string) string {
	base := strings.ToLower(strings.TrimSpace(strings.Split(mt, ";")[0]))
	switch {
	case strings.HasPrefix(base, "image/") && base != "image/svg+xml":
		return base
	case strings.HasPrefix(base, "video/"), strings.HasPrefix(base, "audio/"), base == "application/pdf":
		return base
	}
	return "application/octet-stream"
}

// /media-enc/{cuenta}/{sala}/{evento}?thumb=1
func (s *Server) handleEncMedia(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/media-enc/"), "/", 3)
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	acctID, _ := url.PathUnescape(parts[0])
	roomID, _ := url.PathUnescape(parts[1])
	eventID, _ := url.PathUnescape(parts[2])
	a, err := s.m.Account(acctID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	res, err := a.FetchEncryptedMedia(r.Context(), roomID, eventID, r.URL.Query().Get("thumb") == "1")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if res.Path != "" {
		s.serveLocalFile(w, r, res)
		return
	}
	w.Header().Set("Content-Type", sanitizeMime(res.Mime))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if r.URL.Query().Get("dl") == "1" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": res.Name}))
	}
	http.ServeContent(w, r, res.Name, time.Time{}, strings.NewReader(string(res.Data)))
}

// /file/{cuenta}/{sala}/{evento}: el archivo completo de un mensaje (cualquier tamaño, cifrado o no,
// o dividido en partes), descargado, verificado y servido desde disco con soporte de rangos.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/file/"), "/", 3)
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	acctID, _ := url.PathUnescape(parts[0])
	roomID, _ := url.PathUnescape(parts[1])
	eventID, _ := url.PathUnescape(parts[2])
	a, err := s.m.Account(acctID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	res, err := a.FileOf(r.Context(), roomID, eventID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.serveLocalFile(w, r, res)
}

func (s *Server) serveLocalFile(w http.ResponseWriter, r *http.Request, res *core.MediaResult) {
	w.Header().Set("Content-Type", sanitizeMime(res.Mime))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; media-src 'self'")
	if r.URL.Query().Get("dl") == "1" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": res.Name}))
	}
	f, err := os.Open(res.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	http.ServeContent(w, r, res.Name, st.ModTime(), f)
}

// /upload?a=&room=&mode=send|plain|room&name=&mime=&caption=&reply=&thread=
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método no permitido", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	a, err := s.m.Account(q.Get("a"))
	if err != nil {
		writeJSONErr(w, err)
		return
	}
	name := q.Get("name")
	if name == "" {
		name = "archivo"
	}
	mt := q.Get("mime")
	var result any
	switch q.Get("mode") {
	case "plain", "room":
		// Avatares, emojis y similares: chicos, en memoria.
		data, rerr := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if rerr != nil {
			writeJSONErr(w, rerr)
			return
		}
		if mt == "" {
			mt = http.DetectContentType(data)
		}
		if q.Get("mode") == "plain" {
			var u string
			u, err = a.UploadPlain(r.Context(), data, mt, name)
			result = map[string]string{"url": u}
		} else {
			result, err = a.Upload(r.Context(), q.Get("room"), data, mt, name)
		}
	default:
		// Archivos para enviar: directo a disco (nunca enteros en memoria). Si no entra en el servidor,
		// se avisa antes de copiar nada.
		if want, perr := strconv.ParseInt(q.Get("size"), 10, 64); perr == nil {
			if err := a.CheckUploadSize(r.Context(), want); err != nil {
				writeJSONErr(w, err)
				return
			}
		}
		path, n, sniff, rerr := s.spoolUpload(r.Body)
		if rerr != nil {
			writeJSONErr(w, rerr)
			return
		}
		if want := q.Get("size"); want != "" && want != strconv.FormatInt(n, 10) {
			os.Remove(path)
			writeJSONErr(w, fmt.Errorf("el archivo llegó incompleto (%d de %s bytes)", n, want))
			return
		}
		if mt == "" {
			mt = http.DetectContentType(sniff)
		}
		var extra map[string]any
		if ex := q.Get("extra"); ex != "" {
			_ = json.Unmarshal([]byte(ex), &extra)
		}
		var txn string
		txn, err = a.SendFile(r.Context(), q.Get("room"), path, mt, name, q.Get("caption"), extra, q.Get("reply"), q.Get("thread"))
		if err != nil {
			os.Remove(path)
		}
		result = map[string]string{"txn": txn}
	}
	if err != nil {
		writeJSONErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// spoolUpload guarda el cuerpo de la subida en un temporal privado y devuelve sus primeros bytes.
func (s *Server) spoolUpload(body io.Reader) (string, int64, []byte, error) {
	dir := filepath.Join(s.m.Paths.Cache, "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, nil, err
	}
	f, err := os.CreateTemp(dir, "up-*")
	if err != nil {
		return "", 0, nil, err
	}
	head := make([]byte, 512)
	hn, _ := io.ReadFull(body, head)
	head = head[:hn]
	if _, err := f.Write(head); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", 0, nil, err
	}
	n, err := io.Copy(f, body)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", 0, nil, fmt.Errorf("no se pudo recibir el archivo: %w", err)
	}
	return f.Name(), n + int64(hn), head, nil
}

func writeJSONErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (s *Server) handleExportKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método no permitido", http.StatusMethodNotAllowed)
		return
	}
	a, err := s.m.Account(r.URL.Query().Get("a"))
	if err != nil {
		writeJSONErr(w, err)
		return
	}
	pass, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	data, err := a.ExportKeys(r.Context(), string(pass))
	if err != nil {
		writeJSONErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", `attachment; filename="hilvan-claves.txt"`)
	_, _ = w.Write(data)
}

// --- callbacks de inicio de sesión (abiertos desde el navegador del sistema) ---

func callbackPage(w http.ResponseWriter, ok bool, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	title := "¡Listo!"
	color := "#2e7d5b"
	if !ok {
		title = "No se pudo iniciar sesión"
		color = "#b3261e"
	}
	fmt.Fprintf(w, `<!doctype html><html lang="es"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Hilvan</title><body style="font-family:system-ui,sans-serif;display:grid;place-items:center;min-height:90vh;background:#f6f7fb;color:#1b1d23">
<div style="text-align:center;max-width:420px;padding:24px"><h1 style="color:%s;font-weight:600">%s</h1><p>%s</p></div></body></html>`,
		color, html.EscapeString(title), html.EscapeString(msg))
}

func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if _, err := s.m.CompleteSSO(q.Get("state"), q.Get("loginToken")); err != nil {
		callbackPage(w, false, err.Error())
		return
	}
	callbackPage(w, true, "Ya podés cerrar esta pestaña y volver a Hilvan.")
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if _, err := s.m.CompleteOAuth(q.Get("state"), q.Get("code"), q.Get("error")); err != nil {
		callbackPage(w, false, err.Error())
		return
	}
	callbackPage(w, true, "Ya podés cerrar esta pestaña y volver a Hilvan.")
}
