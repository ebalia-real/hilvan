package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"

	"hilvan/internal/media"
)

type SendParams struct {
	Room        string            `json:"room"`
	Text        string            `json:"text"`
	HTML        string            `json:"html,omitempty"`
	Markdown    bool              `json:"markdown"`
	MsgType     string            `json:"msgtype,omitempty"`
	ReplyTo     string            `json:"reply_to,omitempty"`
	Thread      string            `json:"thread,omitempty"`
	Edit        string            `json:"edit,omitempty"`
	Mentions    []string          `json:"mentions,omitempty"`
	RoomMention bool              `json:"room_mention,omitempty"`
	Extra       json.RawMessage   `json:"extra,omitempty"`
	Type        string            `json:"type,omitempty"` // tipo de evento crudo (por defecto m.room.message)
	Content     json.RawMessage   `json:"content,omitempty"`
	Emotes      map[string]string `json:"emotes,omitempty"` // :atajo: -> mxc
}

var emoteRe = regexp.MustCompile(`:([a-zA-Z0-9_\-+.]{1,64}):`)

// BuildContent arma el contenido del mensaje (markdown, respuesta, hilo, edición, menciones).
func (a *Account) BuildContent(p *SendParams) (event.Type, map[string]any, error) {
	if p.Type != "" && len(p.Content) > 0 {
		var c map[string]any
		if err := json.Unmarshal(p.Content, &c); err != nil {
			return event.Type{}, nil, err
		}
		return event.Type{Type: p.Type, Class: event.MessageEventType}, c, nil
	}
	text := strings.TrimRight(p.Text, " \n\t")
	if text == "" && p.HTML == "" {
		return event.Type{}, nil, errors.New("mensaje vacío")
	}
	var mc event.MessageEventContent
	if p.HTML != "" {
		mc = event.MessageEventContent{MsgType: event.MsgText, Body: text, Format: event.FormatHTML, FormattedBody: p.HTML}
	} else if p.Markdown {
		mc = format.RenderMarkdown(text, true, false)
	} else {
		mc = format.TextToContent(text)
	}
	// Emojis personalizados (MSC2545) dentro del texto.
	if len(p.Emotes) > 0 {
		html := mc.FormattedBody
		if html == "" {
			html = escapeHTML(mc.Body)
		}
		replaced := emoteRe.ReplaceAllStringFunc(html, func(m string) string {
			code := strings.Trim(m, ":")
			if url, ok := p.Emotes[code]; ok && strings.HasPrefix(url, "mxc://") {
				return fmt.Sprintf(`<img data-mx-emoticon src="%s" alt=":%s:" title=":%s:" height="32" />`, url, code, code)
			}
			return m
		})
		if replaced != html {
			mc.Format, mc.FormattedBody = event.FormatHTML, replaced
		}
	}
	if p.MsgType != "" {
		mc.MsgType = event.MessageType(p.MsgType)
	}
	content := map[string]any{}
	b, _ := json.Marshal(&mc)
	_ = json.Unmarshal(b, &content)

	mentions := map[string]any{}
	if len(p.Mentions) > 0 {
		mentions["user_ids"] = p.Mentions
	}
	if p.RoomMention {
		mentions["room"] = true
	}
	// Al responder, se menciona al autor original (spec 1.7+).
	if p.ReplyTo != "" {
		if orig := a.store.GetEvent(p.ReplyTo); orig != nil && orig.Sender != a.UserID() {
			ids, _ := mentions["user_ids"].([]string)
			found := false
			for _, u := range ids {
				if u == orig.Sender {
					found = true
				}
			}
			if !found {
				mentions["user_ids"] = append(ids, orig.Sender)
			}
		}
	}
	content["m.mentions"] = mentions

	if len(p.Extra) > 0 {
		var extra map[string]any
		if json.Unmarshal(p.Extra, &extra) == nil {
			for k, v := range extra {
				content[k] = v
			}
		}
	}

	if p.Edit != "" {
		newContent := map[string]any{}
		for k, v := range content {
			newContent[k] = v
		}
		edit := map[string]any{
			"msgtype":       content["msgtype"],
			"body":          "* " + mc.Body,
			"m.new_content": newContent,
			"m.relates_to":  map[string]any{"rel_type": "m.replace", "event_id": p.Edit},
			"m.mentions":    mentions,
		}
		if mc.FormattedBody != "" {
			edit["format"] = "org.matrix.custom.html"
			edit["formatted_body"] = "* " + mc.FormattedBody
		}
		return event.EventMessage, edit, nil
	}

	switch {
	case p.Thread != "":
		rel := map[string]any{"rel_type": "m.thread", "event_id": p.Thread}
		if p.ReplyTo != "" && p.ReplyTo != p.Thread {
			rel["m.in_reply_to"] = map[string]any{"event_id": p.ReplyTo}
			rel["is_falling_back"] = false
		} else {
			// Respaldo para clientes sin hilos: responde al último mensaje del hilo.
			latest := p.Thread
			if rows := a.store.ThreadEvents(p.Thread, 1); len(rows) > 0 {
				latest = rows[len(rows)-1].EventID
			}
			rel["m.in_reply_to"] = map[string]any{"event_id": latest}
			rel["is_falling_back"] = true
		}
		content["m.relates_to"] = rel
	case p.ReplyTo != "":
		content["m.relates_to"] = map[string]any{"m.in_reply_to": map[string]any{"event_id": p.ReplyTo}}
	}
	return event.EventMessage, content, nil
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "\n", "<br/>")
	return r.Replace(s)
}

// Send envía con eco local inmediato (la interfaz lo muestra al instante).
func (a *Account) Send(ctx context.Context, p *SendParams) (string, error) {
	evtType, content, err := a.BuildContent(p)
	if err != nil {
		return "", err
	}
	return a.sendEvent(p.Room, evtType, content)
}

func (a *Account) sendEvent(roomID string, evtType event.Type, content map[string]any) (string, error) {
	return a.sendEventPrepared(roomID, evtType, content, nil, nil)
}

// sendJob es un envío en la cola FIFO de una sala (preserva el orden de los mensajes).
type sendJob struct {
	row     *EventRow
	evtType event.Type
	content map[string]any
	prepare func(ctx context.Context, txn string) (map[string]any, error) // p. ej. subir el archivo
	cleanup func()                                                        // al terminar bien (borrar temporales)
}

func (a *Account) sendEventPrepared(roomID string, evtType event.Type, content map[string]any,
	prepare func(ctx context.Context, txn string) (map[string]any, error), cleanup func()) (string, error) {
	txn := "hv" + a.cli.TxnID()
	raw, _ := json.Marshal(content)
	row := &EventRow{RoomID: roomID, EventID: "~" + txn, TxnID: txn, Sender: a.UserID(), Type: evtType.Type,
		TS: time.Now().UnixMilli(), Content: raw, Status: "sending"}
	fillRelations(row)
	if enc, _ := a.stateStore.IsEncrypted(a.ctx, id.RoomID(roomID)); enc {
		row.Encrypted = json.RawMessage(`{}`)
	}
	a.store.PutEvent(row)
	a.pushRowUpdate(row, true)
	a.enqueue(&sendJob{row: row, evtType: evtType, content: content, prepare: prepare, cleanup: cleanup})
	return txn, nil
}

func (a *Account) enqueue(job *sendJob) {
	a.sendMu.Lock()
	q, ok := a.sendQueues[job.row.RoomID]
	if !ok {
		q = make(chan *sendJob, 4096)
		a.sendQueues[job.row.RoomID] = q
		go func() {
			for {
				select {
				case j := <-q:
					a.deliver(j)
				case <-a.ctx.Done():
					return
				}
			}
		}()
	}
	a.sendMu.Unlock()
	q <- job
}

func (a *Account) pushRowUpdate(row *EventRow, isNew bool) {
	batch := newSyncBatch()
	switch {
	case row.RelTo != "" && (row.RelType == "m.annotation" || row.RelType == "m.replace" || row.RelType == "m.reference"):
		batch.touch(row.RoomID, row.RelTo)
	case isNew:
		batch.newItems[row.RoomID] = append(batch.newItems[row.RoomID], row.EventID)
		if row.ThreadRoot != "" {
			batch.touch(row.RoomID, row.ThreadRoot)
		}
	default:
		batch.touch(row.RoomID, row.EventID)
	}
	batch.rooms[row.RoomID] = true
	a.flushBatch(batch)
}

func (a *Account) deliver(job *sendJob) {
	row := job.row
	fail := func(err error) {
		a.log.Warn().Err(err).Str("room", row.RoomID).Msg("Falló el envío")
		a.store.SetEventStatus(row.EventID, "failed")
		row.Status = "failed"
		a.pushRowUpdate(row, false)
		a.m.Broadcast(Push{Ev: "send.error", A: a.UserID(), D: map[string]string{"room": row.RoomID, "txn": row.TxnID, "error": friendlyError(err).Error()}})
	}
	content := job.content
	if job.prepare != nil {
		// Sin tope de tiempo: un archivo de varios GB puede tardar horas. Si la conexión se cuelga,
		// lo detecta la subida misma (watchStall) y reintenta.
		c, err := job.prepare(a.ctx, row.TxnID)
		if err != nil {
			fail(err)
			return
		}
		content = c
		job.content, job.prepare = c, nil
		row.Content, _ = json.Marshal(c)
		a.store.PutEvent(row)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	resp, err := a.sendWithEncryption(ctx, row.RoomID, job.evtType, content, row.TxnID)
	if err != nil {
		fail(err)
		return
	}
	if job.cleanup != nil {
		job.cleanup()
	}
	// El eco del sync puede llegar antes o después; SetEventIDForTxn maneja ambos casos.
	a.store.SetEventIDForTxn(row.TxnID, resp.EventID.String())
	if e := a.store.GetEvent(resp.EventID.String()); e != nil {
		batch := newSyncBatch()
		batch.touch(row.RoomID, e.EventID)
		if e.RelTo != "" {
			batch.touch(row.RoomID, e.RelTo)
		}
		batch.rooms[row.RoomID] = true
		a.flushBatch(batch)
		a.m.Broadcast(Push{Ev: "sent", A: a.UserID(), D: map[string]string{"room": row.RoomID, "txn": row.TxnID, "id": e.EventID}})
	}
}

// sendWithEncryption cifra también las reacciones (mautrix no lo hace por defecto).
func (a *Account) sendWithEncryption(ctx context.Context, roomID string, evtType event.Type, content any, txn string) (*mautrix.RespSendEvent, error) {
	rid := id.RoomID(roomID)
	if enc, _ := a.stateStore.IsEncrypted(ctx, rid); enc {
		if err := a.ensureMembers(ctx, roomID); err != nil {
			return nil, err
		}
		encrypted, err := a.cli.Crypto.Encrypt(ctx, rid, evtType, content)
		if err != nil {
			return nil, err
		}
		a.kickBackup()
		return a.cli.SendMessageEvent(ctx, rid, event.EventEncrypted, encrypted, mautrix.ReqSendEvent{TransactionID: txn, DontEncrypt: true})
	}
	return a.cli.SendMessageEvent(ctx, rid, evtType, content, mautrix.ReqSendEvent{TransactionID: txn, DontEncrypt: true})
}

// Retry reenvía un mensaje fallido.
func (a *Account) Retry(eventID string) error {
	row := a.store.GetEvent(eventID)
	if row == nil || row.Status != "failed" {
		return errors.New("no hay nada que reintentar")
	}
	var content map[string]any
	_ = json.Unmarshal(row.Content, &content)
	a.store.SetEventStatus(eventID, "sending")
	row.Status = "sending"
	a.pushRowUpdate(row, false)
	if _, hasURL := content["url"]; !hasURL {
		if _, hasFile := content["file"]; !hasFile {
			if mt, _ := content["msgtype"].(string); mt == "m.image" || mt == "m.video" || mt == "m.audio" || mt == "m.file" {
				return errors.New("el archivo no se llegó a subir; volvé a adjuntarlo")
			}
		}
	}
	a.enqueue(&sendJob{row: row, evtType: event.Type{Type: row.Type, Class: event.MessageEventType}, content: content})
	return nil
}

func (a *Account) DiscardLocal(eventID string) {
	if row := a.store.GetEvent(eventID); row != nil && strings.HasPrefix(eventID, "~") {
		a.store.DeleteEvent(eventID)
		a.m.Broadcast(Push{Ev: "local.removed", A: a.UserID(), D: map[string]string{"room": row.RoomID, "id": eventID}})
	}
}

// resendPending reintenta al arrancar lo que quedó "enviando" al cerrar la app.
func (a *Account) resendPending() {
	time.Sleep(3 * time.Second)
	for _, row := range a.store.PendingEvents() {
		a.store.SetEventStatus(row.EventID, "failed")
		row.Status = "failed"
		_ = a.Retry(row.EventID)
	}
}

func (a *Account) React(ctx context.Context, roomID, eventID, key string) (string, error) {
	// Si ya reaccionamos con esa clave, la quitamos (comportamiento "toggle").
	for _, rel := range a.store.Relations([]string{eventID}) {
		if rel.RelType == "m.annotation" && rel.RelKey == key && rel.Sender == a.UserID() {
			return "", a.Redact(ctx, roomID, rel.EventID, "")
		}
	}
	content := map[string]any{"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": eventID, "key": key}}
	if strings.HasPrefix(key, "mxc://") {
		content["shortcode"] = ""
	}
	return a.sendEvent(roomID, event.EventReaction, content)
}

func (a *Account) Redact(ctx context.Context, roomID, eventID, reason string) error {
	if strings.HasPrefix(eventID, "~") {
		a.DiscardLocal(eventID)
		return nil
	}
	// Redacción optimista: la interfaz lo oculta ya.
	batch := newSyncBatch()
	if t := a.store.GetEvent(eventID); t != nil {
		a.applyRedaction(roomID, eventID, "~pending", batch)
		a.flushBatch(batch)
	}
	_, err := a.cli.RedactEvent(ctx, id.RoomID(roomID), id.EventID(eventID), mautrix.ReqRedact{Reason: reason})
	return friendlyErrorNil(err)
}

func (a *Account) MarkRead(ctx context.Context, roomID, eventID string) error {
	if eventID == "" || strings.HasPrefix(eventID, "~") {
		_, eventID = a.store.MaxSortEvent(roomID)
	}
	if eventID == "" {
		return nil
	}
	e := a.store.GetEvent(eventID)
	r := a.store.GetRoom(roomID)
	if e != nil && r != nil && (e.Sort > r.ReadSort || r.MarkedUnread || r.LocalHighlight > 0) {
		a.store.UpdateRoom(roomID, map[string]any{"read_sort": e.Sort, "read_event": eventID, "local_highlight": 0, "marked_unread": 0, "srv_notif": 0, "srv_highlight": 0})
		batch := newSyncBatch()
		batch.rooms[roomID] = true
		a.flushBatch(batch)
	}
	a.m.Notifier.Close(a.UserID(), roomID)
	s := a.m.Settings()
	content := map[string]any{"m.fully_read": eventID}
	if s.SendReadReceipts && !s.PrivateReceipts {
		content["m.read"] = eventID
	} else {
		content["m.read.private"] = eventID
	}
	if r != nil && r.MarkedUnread {
		_ = a.cli.SetRoomAccountData(ctx, id.RoomID(roomID), "m.marked_unread", map[string]bool{"unread": false})
	}
	return a.cli.SetReadMarkers(ctx, id.RoomID(roomID), content)
}

func (a *Account) MarkUnread(ctx context.Context, roomID string, unread bool) error {
	a.store.UpdateRoom(roomID, map[string]any{"marked_unread": unread})
	batch := newSyncBatch()
	batch.rooms[roomID] = true
	a.flushBatch(batch)
	return a.cli.SetRoomAccountData(ctx, id.RoomID(roomID), "m.marked_unread", map[string]bool{"unread": unread})
}

func (a *Account) Typing(ctx context.Context, roomID string, typing bool) error {
	if !a.m.Settings().SendTyping {
		return nil
	}
	_, err := a.cli.UserTyping(ctx, id.RoomID(roomID), typing, 20*time.Second)
	return err
}

// --- multimedia ---

type UploadResult struct {
	URL       string          `json:"url,omitempty"`
	File      json.RawMessage `json:"file,omitempty"`
	ThumbURL  string          `json:"thumb_url,omitempty"`
	ThumbFile json.RawMessage `json:"thumb_file,omitempty"`
	Info      map[string]any  `json:"info"`
}

// Upload sube un archivo (cifrado si la sala está cifrada) y genera miniatura y dimensiones para imágenes.
func (a *Account) Upload(ctx context.Context, roomID string, data []byte, mime, name string) (*UploadResult, error) {
	encrypted := false
	if roomID != "" {
		encrypted, _ = a.stateStore.IsEncrypted(ctx, id.RoomID(roomID))
	}
	res := &UploadResult{Info: map[string]any{"mimetype": mime, "size": len(data)}}
	if strings.HasPrefix(mime, "image/") && mime != "image/svg+xml" {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			res.Info["w"], res.Info["h"] = cfg.Width, cfg.Height
			if (cfg.Width > 800 || cfg.Height > 600 || len(data) > 512*1024) && mime != "image/gif" {
				if thumb, tw, th, err := makeThumbnail(data, 800, 600); err == nil {
					url, file, err := a.uploadBytes(ctx, thumb, "image/jpeg", "thumbnail.jpg", encrypted)
					if err == nil {
						res.ThumbURL, res.ThumbFile = url, file
						res.Info["thumbnail_info"] = map[string]any{"w": tw, "h": th, "mimetype": "image/jpeg", "size": len(thumb)}
					}
				}
			}
			if bh := blurhashOf(data); bh != "" {
				res.Info["xyz.amorgan.blurhash"] = bh
			}
		}
	}
	url, file, err := a.uploadBytes(ctx, data, mime, name, encrypted)
	if err != nil {
		return nil, err
	}
	res.URL, res.File = url, file
	if res.ThumbFile != nil {
		res.Info["thumbnail_file"] = json.RawMessage(res.ThumbFile)
	} else if res.ThumbURL != "" {
		res.Info["thumbnail_url"] = res.ThumbURL
	}
	return res, nil
}

func (a *Account) uploadBytes(ctx context.Context, data []byte, mime, name string, encrypted bool) (string, json.RawMessage, error) {
	if encrypted {
		ef := attachment.NewEncryptedFile()
		cipher := make([]byte, len(data))
		copy(cipher, data)
		ef.EncryptInPlace(cipher)
		resp, err := a.cli.UploadMedia(ctx, mautrix.ReqUploadMedia{ContentBytes: cipher, ContentType: "application/octet-stream", FileName: ""})
		if err != nil {
			return "", nil, friendlyError(err)
		}
		info := event.EncryptedFileInfo{EncryptedFile: *ef, URL: resp.ContentURI.CUString()}
		b, _ := json.Marshal(info)
		return "", b, nil
	}
	resp, err := a.cli.UploadMedia(ctx, mautrix.ReqUploadMedia{ContentBytes: data, ContentType: mime, FileName: name})
	if err != nil {
		return "", nil, friendlyError(err)
	}
	return resp.ContentURI.String(), nil, nil
}

func makeThumbnail(data []byte, maxW, maxH int) ([]byte, int, int, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := min(float64(maxW)/float64(w), float64(maxH)/float64(h), 1)
	tw, th := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), tw, th, nil
}

// SendFile muestra el archivo al instante (eco local) y lo sube y envía en orden dentro de la cola de la sala.
// path es un archivo temporal propio (se borra cuando el envío termina bien). Sin límite de tamaño:
// se sube en streaming y, si supera el tope del servidor, en partes verificables.
func (a *Account) SendFile(ctx context.Context, roomID, path, mime, name, caption string, extra map[string]any, replyTo, thread string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	size := st.Size()
	if err := a.CheckUploadSize(ctx, size); err != nil {
		return "", err
	}
	// Imágenes razonables: compresión opcional, dimensiones, miniatura y blurhash (en memoria).
	var imgData []byte
	if strings.HasPrefix(mime, "image/") && size <= 64<<20 {
		if b, err := os.ReadFile(path); err == nil {
			imgData = b
			if a.m.Settings().CompressImages {
				if nd, nm, changed := compressImage(b, mime); changed {
					if err := os.WriteFile(path, nd, 0o600); err == nil {
						imgData, mime, size = nd, nm, int64(len(nd))
						if i := strings.LastIndex(name, "."); i > 0 {
							name = name[:i] + ".jpg"
						}
					}
				}
			}
		}
	}
	msgtype := "m.file"
	switch {
	case strings.HasPrefix(mime, "image/"):
		msgtype = "m.image"
	case strings.HasPrefix(mime, "video/"):
		msgtype = "m.video"
	case strings.HasPrefix(mime, "audio/"):
		msgtype = "m.audio"
	}
	info := map[string]any{"mimetype": mime, "size": size}
	if imgData != nil {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(imgData)); err == nil {
			info["w"], info["h"] = cfg.Width, cfg.Height
		}
	}
	// Videos: tamaño, duración y un cuadro de miniatura, para mostrarlos como en Telegram.
	var vthumb []byte
	if msgtype == "m.video" {
		var w, h int
		var dur time.Duration
		if vthumb, w, h, dur = videoInfo(path); w > 0 && h > 0 {
			info["w"], info["h"] = w, h
		}
		if dur > 0 {
			info["duration"] = dur.Milliseconds()
		}
	}
	if ei, ok := extra["info"].(map[string]any); ok {
		for k, v := range ei {
			info[k] = v
		}
	}
	content := map[string]any{"msgtype": msgtype, "body": name, "filename": name, "info": info, "m.mentions": map[string]any{}}
	if caption != "" {
		content["body"] = caption
		mc := format.RenderMarkdown(caption, true, false)
		if mc.FormattedBody != "" {
			content["format"], content["formatted_body"] = "org.matrix.custom.html", mc.FormattedBody
		}
	}
	for k, v := range extra {
		if k != "info" {
			content[k] = v
		}
	}
	if thread != "" {
		content["m.relates_to"] = map[string]any{"rel_type": "m.thread", "event_id": thread, "is_falling_back": true,
			"m.in_reply_to": map[string]any{"event_id": thread}}
	} else if replyTo != "" {
		content["m.relates_to"] = map[string]any{"m.in_reply_to": map[string]any{"event_id": replyTo}}
	}
	prepare := func(ctx context.Context, txn string) (map[string]any, error) {
		encrypted, _ := a.stateStore.IsEncrypted(ctx, id.RoomID(roomID))
		final := map[string]any{}
		for k, v := range content {
			final[k] = v
		}
		fi := map[string]any{}
		for k, v := range info {
			fi[k] = v
		}
		if imgData != nil {
			if extraInfo := a.imageExtras(ctx, imgData, mime, encrypted); extraInfo != nil {
				for k, v := range extraInfo {
					fi[k] = v
				}
			}
		}
		if vthumb != nil {
			for k, v := range a.thumbExtras(ctx, vthumb, encrypted) {
				fi[k] = v
			}
		}
		t := &Transfer{ID: txn, Room: roomID, Name: name, Dir: "up", Total: size, State: "running"}
		a.pushTransfer(t, true)
		part, err := a.uploadFile(ctx, path, size, mime, name, encrypted, t)
		if err != nil {
			t.State, t.Error = "error", friendlyError(err).Error()
			a.pushTransfer(t, true)
			return nil, err
		}
		t.State, t.Done = "done", size
		a.pushTransfer(t, true)
		final["info"] = fi
		if part.File != nil {
			final["file"] = part.File
		} else {
			final["url"] = part.URL
		}
		return final, nil
	}
	return a.sendEventPrepared(roomID, event.EventMessage, content, prepare, func() { _ = os.Remove(path) })
}

// videoInfo saca de un video su tamaño, su duración y un cuadro JPEG para la miniatura.
func videoInfo(path string) (thumb []byte, w, h int, dur time.Duration) {
	f, err := os.CreateTemp("", "hilvan-cuadro-*.jpg")
	if err != nil {
		return nil, 0, 0, 0
	}
	out := f.Name()
	f.Close()
	defer os.Remove(out)
	w, h, dur, err = media.VideoThumb(path, out, 800, 600, 5*time.Second)
	if err == nil {
		thumb, _ = os.ReadFile(out)
	}
	return thumb, w, h, dur
}

// thumbExtras sube una miniatura ya hecha (JPEG) y devuelve los campos de info que la describen.
func (a *Account) thumbExtras(ctx context.Context, thumb []byte, encrypted bool) map[string]any {
	out := map[string]any{}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(thumb))
	if err != nil {
		return out
	}
	url, file, err := a.uploadBytes(ctx, thumb, "image/jpeg", "thumbnail.jpg", encrypted)
	if err != nil {
		return out
	}
	if file != nil {
		out["thumbnail_file"] = json.RawMessage(file)
	} else {
		out["thumbnail_url"] = url
	}
	out["thumbnail_info"] = map[string]any{"w": cfg.Width, "h": cfg.Height, "mimetype": "image/jpeg", "size": len(thumb)}
	if bh := blurhashOf(thumb); bh != "" {
		out["xyz.amorgan.blurhash"] = bh
	}
	return out
}

// imageExtras genera miniatura y blurhash de una imagen.
func (a *Account) imageExtras(ctx context.Context, data []byte, mime string, encrypted bool) map[string]any {
	out := map[string]any{}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	if (cfg.Width > 800 || cfg.Height > 600 || len(data) > 512*1024) && mime != "image/gif" {
		if thumb, tw, th, err := makeThumbnail(data, 800, 600); err == nil {
			url, file, err := a.uploadBytes(ctx, thumb, "image/jpeg", "thumbnail.jpg", encrypted)
			if err == nil {
				if file != nil {
					out["thumbnail_file"] = json.RawMessage(file)
				} else {
					out["thumbnail_url"] = url
				}
				out["thumbnail_info"] = map[string]any{"w": tw, "h": th, "mimetype": "image/jpeg", "size": len(thumb)}
			}
		}
	}
	if bh := blurhashOf(data); bh != "" {
		out["xyz.amorgan.blurhash"] = bh
	}
	return out
}

// --- encuestas (MSC3381) ---

func (a *Account) SendPoll(roomID, question string, answers []string, maxSel int, disclosed bool) (string, error) {
	if question == "" || len(answers) < 2 {
		return "", errors.New("la encuesta necesita una pregunta y al menos dos opciones")
	}
	kind := "org.matrix.msc3381.poll.undisclosed"
	if disclosed {
		kind = "org.matrix.msc3381.poll.disclosed"
	}
	var ans []map[string]any
	fallback := question
	for i, t := range answers {
		aid := fmt.Sprintf("a%d", i+1)
		ans = append(ans, map[string]any{"id": aid, "org.matrix.msc1767.text": t})
		fallback += fmt.Sprintf("\n%d. %s", i+1, t)
	}
	if maxSel <= 0 {
		maxSel = 1
	}
	content := map[string]any{
		"org.matrix.msc3381.poll.start": map[string]any{
			"kind": kind, "max_selections": maxSel,
			"question": map[string]any{"org.matrix.msc1767.text": question, "body": question, "msgtype": "m.text"},
			"answers":  ans,
		},
		"org.matrix.msc1767.text": fallback,
		"body":                    fallback,
	}
	return a.sendEvent(roomID, event.Type{Type: "org.matrix.msc3381.poll.start", Class: event.MessageEventType}, content)
}

func (a *Account) VotePoll(roomID, pollID string, answers []string) (string, error) {
	content := map[string]any{
		"m.relates_to":                     map[string]any{"rel_type": "m.reference", "event_id": pollID},
		"org.matrix.msc3381.poll.response": map[string]any{"answers": answers},
	}
	return a.sendEvent(roomID, event.Type{Type: "org.matrix.msc3381.poll.response", Class: event.MessageEventType}, content)
}

func (a *Account) EndPoll(roomID, pollID string) (string, error) {
	content := map[string]any{
		"m.relates_to":                map[string]any{"rel_type": "m.reference", "event_id": pollID},
		"org.matrix.msc3381.poll.end": map[string]any{},
		"org.matrix.msc1767.text":     "La encuesta terminó",
		"body":                        "La encuesta terminó",
	}
	return a.sendEvent(roomID, event.Type{Type: "org.matrix.msc3381.poll.end", Class: event.MessageEventType}, content)
}

// --- mensajes programados (local) ---

func (a *Account) Schedule(p *SendParams, at int64) (int64, error) {
	if at <= time.Now().UnixMilli() {
		return 0, errors.New("elegí un momento futuro")
	}
	b, _ := json.Marshal(p)
	sid := a.store.AddScheduled(p.Room, at, b)
	a.m.Broadcast(Push{Ev: "scheduled", A: a.UserID(), D: a.store.ListScheduled()})
	return sid, nil
}

func (a *Account) schedulerLoop() {
	a.wg.Add(1)
	defer a.wg.Done()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
			now := time.Now().UnixMilli()
			changed := false
			for _, sc := range a.store.ListScheduled() {
				if sc.SendAt > now {
					continue
				}
				var p SendParams
				if json.Unmarshal(sc.Payload, &p) == nil {
					if _, err := a.Send(a.ctx, &p); err != nil {
						a.log.Warn().Err(err).Msg("No se pudo enviar un mensaje programado")
					}
				}
				a.store.DeleteScheduled(sc.ID)
				changed = true
			}
			if changed {
				a.m.Broadcast(Push{Ev: "scheduled", A: a.UserID(), D: a.store.ListScheduled()})
			}
		}
	}
}

// pinned devuelve los mensajes fijados.
func (a *Account) Pinned(ctx context.Context, roomID string) ([]Item, map[string]Profile) {
	pins := a.store.GetState(roomID, "m.room.pinned_events", "")
	if pins == nil {
		return nil, nil
	}
	var rows []*EventRow
	for _, p := range gjson.GetBytes(pins.Content, "pinned").Array() {
		if e := a.store.GetEvent(p.Str); e != nil {
			rows = append(rows, e)
		} else if evt, err := a.cli.GetEvent(ctx, id.RoomID(roomID), id.EventID(p.Str)); err == nil {
			rows = append(rows, a.prepareEvent(ctx, roomID, evt).row)
		}
	}
	return a.buildItems(roomID, rows)
}
