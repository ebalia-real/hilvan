package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Exportación para auditoría y cumplimiento: historia completa de una sala (también cifrada),
// con ediciones, borrados, hilos, estado de cifrado y verificación, más un archivo .sha256
// para comprobar que nadie la modificó después.

type ExportParams struct {
	Room        string `json:"room"`
	Format      string `json:"format"` // html | json | txt
	From        int64  `json:"from"`   // ms (0 = desde el principio)
	To          int64  `json:"to"`     // ms (0 = hasta ahora)
	Attachments bool   `json:"attachments"`
}

type exportEvent struct {
	EventID    string          `json:"event_id"`
	TS         int64           `json:"origin_server_ts"`
	Time       string          `json:"time"`
	Sender     string          `json:"sender"`
	SenderName string          `json:"sender_name,omitempty"`
	Type       string          `json:"type"`
	MsgType    string          `json:"msgtype,omitempty"`
	Body       string          `json:"body,omitempty"`
	Content    json.RawMessage `json:"content"`
	Edits      []exportEdit    `json:"edits,omitempty"`
	Redacted   bool            `json:"redacted,omitempty"`
	Encrypted  bool            `json:"encrypted,omitempty"`
	Trust      string          `json:"trust,omitempty"`
	Undecrypt  string          `json:"undecryptable,omitempty"`
	ReplyTo    string          `json:"reply_to,omitempty"`
	Thread     string          `json:"thread,omitempty"`
	Attachment string          `json:"attachment,omitempty"`
}

type exportEdit struct {
	EventID string `json:"event_id"`
	TS      int64  `json:"origin_server_ts"`
	Body    string `json:"body"`
}

func (a *Account) pushExport(room string, d map[string]any) {
	d["room"] = room
	a.m.Broadcast(Push{Ev: "export", A: a.UserID(), D: d})
}

// ExportRoom arma la exportación y la guarda en Descargas. Devuelve la ruta.
func (a *Account) ExportRoom(ctx context.Context, p ExportParams) (string, error) {
	if a.m.Policy().Disabled("export") {
		return "", errors.New("tu organización desactivó la exportación de chats")
	}
	sum := a.RoomSummary(p.Room)
	if sum == nil {
		return "", errors.New("sala desconocida")
	}
	if p.To <= 0 {
		p.To = time.Now().UnixMilli()
	}
	// 1. Traer la historia completa (o hasta "From") del servidor.
	before := int64(0)
	for {
		page, err := a.Timeline(ctx, p.Room, before, 200)
		if err != nil || len(page.Items) == 0 {
			break
		}
		before = page.Items[0].Sort
		a.pushExport(p.Room, map[string]any{"state": "fetching", "oldest": page.Items[0].TS})
		if !page.HasMore || (p.From > 0 && page.Items[0].TS < p.From) {
			break
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	// 2. Todos los eventos visibles (incluidos hilos) del rango, en orden.
	rows := a.store.RoomEventsRange(p.Room, p.From, p.To)
	items, profs := a.buildItems(p.Room, rows)
	name := func(uid string) string {
		if pr, ok := profs[uid]; ok && pr.Name != "" {
			return pr.Name
		}
		if pr := a.memberProfile(p.Room, uid); pr.Name != "" {
			return pr.Name
		}
		return uid
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	edits := map[string][]exportEdit{}
	for _, rel := range a.store.Relations(ids) {
		if rel.RelType == "m.replace" {
			edits[rel.RelTo] = append(edits[rel.RelTo], exportEdit{EventID: rel.EventID, TS: rel.TS,
				Body: gjson.GetBytes(rel.Content, `m\.new_content.body`).Str})
		}
	}
	base := sanitizeFileName(fmt.Sprintf("Hilvan - %s - %s", nonEmpty(sum.Name, p.Room), time.Now().Format("2006-01-02 15-04")))
	dir := downloadsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	attDir := ""
	if p.Attachments {
		attDir = uniquePath(filepath.Join(dir, base+" (adjuntos)"))
		if err := os.MkdirAll(attDir, 0o755); err != nil {
			return "", err
		}
	}
	var evs []exportEvent
	for i, it := range items {
		c := gjson.ParseBytes(it.Content)
		e := exportEvent{EventID: it.ID, TS: it.TS, Time: time.UnixMilli(it.TS).Format(time.RFC3339), Sender: it.Sender,
			SenderName: name(it.Sender), Type: it.Type, MsgType: c.Get("msgtype").Str, Body: c.Get("body").Str,
			Content: it.Content, Edits: edits[it.ID], Redacted: it.Redacted, Encrypted: it.Enc, Trust: it.Trust,
			Undecrypt: it.UTD, ReplyTo: it.ReplyTo, Thread: it.InThread}
		sort.Slice(e.Edits, func(x, y int) bool { return e.Edits[x].TS < e.Edits[y].TS })
		if attDir != "" && !it.Redacted && (c.Get("url").Exists() || c.Get("file").Exists()) {
			if res, err := a.FileOf(ctx, p.Room, it.ID); err == nil {
				dst := uniquePath(filepath.Join(attDir, sanitizeFileName(nonEmpty(res.Name, it.ID))))
				if copyFile(res.Path, dst) == nil {
					e.Attachment = filepath.Base(attDir) + "/" + filepath.Base(dst)
				}
			}
		}
		evs = append(evs, e)
		if i%200 == 0 {
			a.pushExport(p.Room, map[string]any{"state": "writing", "done": i, "total": len(items)})
		}
	}
	// 3. Huella del contenido (eventos en JSON canónico): queda en el archivo y en el .sha256.
	canon, _ := json.Marshal(evs)
	digest := sha256.Sum256(canon)
	meta := map[string]any{
		"room_id": p.Room, "room_name": sum.Name, "encrypted": sum.Encrypted, "exported_by": a.UserID(),
		"exported_at": time.Now().Format(time.RFC3339), "from": p.From, "to": p.To, "events": len(evs),
		"content_sha256": hex.EncodeToString(digest[:]), "generator": "Hilvan " + a.m.Version,
	}
	var out []byte
	ext := p.Format
	switch p.Format {
	case "json":
		out, _ = json.MarshalIndent(map[string]any{"export": meta, "events": evs}, "", "  ")
	case "txt":
		var b strings.Builder
		fmt.Fprintf(&b, "Exportación de %s (%s)\nExportado por %s el %s · %d eventos · SHA-256 del contenido: %s\n\n",
			nonEmpty(sum.Name, p.Room), p.Room, a.UserID(), meta["exported_at"], len(evs), meta["content_sha256"])
		for _, e := range evs {
			fmt.Fprintf(&b, "[%s] %s: %s\n", time.UnixMilli(e.TS).Format("2006-01-02 15:04:05"), e.SenderName, exportLine(e))
		}
		out = []byte(b.String())
	default:
		ext = "html"
		out = []byte(exportHTML(meta, evs))
	}
	path := uniquePath(filepath.Join(dir, base+"."+ext))
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return "", err
	}
	fileSum := sha256.Sum256(out)
	_ = os.WriteFile(path+".sha256", []byte(hex.EncodeToString(fileSum[:])+"  "+filepath.Base(path)+"\n"), 0o644)
	a.pushExport(p.Room, map[string]any{"state": "done", "path": path, "events": len(evs)})
	return path, nil
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func exportLine(e exportEvent) string {
	switch {
	case e.Redacted:
		return "(mensaje eliminado)"
	case e.Undecrypt != "":
		return "(no se pudo descifrar)"
	case e.Type == "m.room.member":
		return "(" + gjson.GetBytes(e.Content, "membership").Str + ")"
	case e.Attachment != "":
		return e.Body + " [" + e.Attachment + "]"
	}
	s := e.Body
	if len(e.Edits) > 0 {
		s += " (editado)"
	}
	if s == "" {
		s = "(" + e.Type + ")"
	}
	return s
}

func exportHTML(meta map[string]any, evs []exportEvent) string {
	var b strings.Builder
	esc := html.EscapeString
	title := esc(fmt.Sprint(meta["room_name"]))
	b.WriteString(`<!doctype html><html lang="es"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<title>` + title + ` · Exportación</title><style>
body{font:15px/1.5 system-ui,sans-serif;margin:0;background:#f5f6f8;color:#17191d}
header{background:#fff;border-bottom:1px solid #e4e6eb;padding:20px 24px}header h1{margin:0 0 6px;font-size:20px}
.meta{color:#5c6270;font-size:13px}.meta code{font-size:12px}
main{max-width:900px;margin:0 auto;padding:16px 24px}
.ev{background:#fff;border:1px solid #e4e6eb;border-radius:10px;padding:8px 12px;margin:6px 0}
.ev .h{font-size:13px;color:#5c6270}.ev .h b{color:#17191d}.ev .id{font:11px monospace;color:#9aa0ab}
.ev.state{background:transparent;border:0;color:#5c6270;font-size:13px;padding:2px 12px}
.del{color:#9aa0ab;font-style:italic}.utd{color:#c22f2f}.thr{border-left:3px solid #6d5dfc}
.tag{display:inline-block;font-size:11px;border-radius:4px;padding:0 5px;margin-left:6px;background:#eceef2;color:#5c6270}
details{font-size:13px;color:#5c6270}@media print{.ev{break-inside:avoid}}
</style></head><body><header><h1>` + title + `</h1><div class="meta">`)
	fmt.Fprintf(&b, "Sala <code>%s</code> · %d eventos · exportado por %s el %s<br>SHA-256 del contenido: <code>%s</code> · %s",
		esc(fmt.Sprint(meta["room_id"])), meta["events"], esc(fmt.Sprint(meta["exported_by"])), esc(fmt.Sprint(meta["exported_at"])),
		meta["content_sha256"], esc(fmt.Sprint(meta["generator"])))
	b.WriteString(`</div></header><main>`)
	for _, e := range evs {
		t := time.UnixMilli(e.TS).Format("2006-01-02 15:04:05")
		if e.Type != "m.room.message" && e.Type != "m.sticker" && !strings.HasPrefix(e.Type, "org.matrix.msc3381.poll") {
			fmt.Fprintf(&b, `<div class="ev state">%s · %s %s</div>`, t, esc(e.SenderName), esc(exportLine(e)))
			continue
		}
		cls := "ev"
		if e.Thread != "" {
			cls += " thr"
		}
		fmt.Fprintf(&b, `<div class="%s" id="%s"><div class="h"><b>%s</b> <span>%s</span>`, cls, esc(e.EventID), esc(e.SenderName), esc(e.Sender))
		fmt.Fprintf(&b, ` · %s`, t)
		if e.Encrypted {
			b.WriteString(`<span class="tag">cifrado</span>`)
		}
		if e.Trust == "verified" {
			b.WriteString(`<span class="tag">verificado</span>`)
		}
		if len(e.Edits) > 0 {
			b.WriteString(`<span class="tag">editado</span>`)
		}
		b.WriteString(`</div>`)
		switch {
		case e.Redacted:
			b.WriteString(`<div class="del">Mensaje eliminado</div>`)
		case e.Undecrypt != "":
			b.WriteString(`<div class="utd">No se pudo descifrar este mensaje</div>`)
		case e.Attachment != "":
			fmt.Fprintf(&b, `<div>📎 <a href="%s">%s</a></div>`, esc(e.Attachment), esc(e.Body))
		default:
			b.WriteString(`<div>` + strings.ReplaceAll(esc(e.Body), "\n", "<br>") + `</div>`)
		}
		if len(e.Edits) > 0 {
			b.WriteString(`<details><summary>Historial de ediciones</summary>`)
			for _, ed := range e.Edits {
				fmt.Fprintf(&b, `<div>%s · %s</div>`, time.UnixMilli(ed.TS).Format("2006-01-02 15:04:05"), esc(ed.Body))
			}
			b.WriteString(`</details>`)
		}
		fmt.Fprintf(&b, `<div class="id">%s</div></div>`, esc(e.EventID))
	}
	b.WriteString(`</main></body></html>`)
	return b.String()
}
