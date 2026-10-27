package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestExportE2E(t *testing.T) {
	hs := testHS(t)
	t.Setenv("HILVAN_DOWNLOAD_DIR", t.TempDir())
	ctx := context.Background()
	ana := newTester(t, hs, "ana", "prueba-ana-123")
	room, err := ana.a.CreateRoom(ctx, CreateRoomParams{Name: "Auditoría", Encrypted: true})
	if err != nil {
		t.Fatal(err)
	}
	send := func(text string) string {
		txn, err := ana.a.Send(ctx, &SendParams{Room: room, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		var id string
		ana.waitFor("enviado "+text, 30*time.Second, func(p Push) bool {
			for _, it := range itemsIn(p, room) {
				if it.Txn == txn && !strings.HasPrefix(it.ID, "~") {
					id = it.ID
					return true
				}
			}
			return false
		})
		return id
	}
	first := send("primer mensaje")
	second := send("esto se va a borrar")
	if _, err := ana.a.Send(ctx, &SendParams{Room: room, Text: "primer mensaje (corregido)", Edit: first}); err != nil {
		t.Fatal(err)
	}
	if err := ana.a.Redact(ctx, room, second, "prueba"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	path, err := ana.a.ExportRoom(ctx, ExportParams{Room: room, Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	doc := gjson.ParseBytes(b)
	var gotEdit, gotDel bool
	for _, e := range doc.Get("events").Array() {
		if e.Get("event_id").Str == first && e.Get("edits.0.body").Str == "primer mensaje (corregido)" && e.Get("encrypted").Bool() {
			gotEdit = true
		}
		if e.Get("event_id").Str == second && e.Get("redacted").Bool() {
			gotDel = true
		}
	}
	if !gotEdit || !gotDel {
		t.Fatalf("exportación incompleta (edición=%v borrado=%v): %s", gotEdit, gotDel, string(b[:min(800, len(b))]))
	}
	sumFile, _ := os.ReadFile(path + ".sha256")
	h := sha256.Sum256(b)
	if !strings.HasPrefix(string(sumFile), hex.EncodeToString(h[:])) {
		t.Fatal("el .sha256 no corresponde al archivo exportado")
	}
	if p2, err := ana.a.ExportRoom(ctx, ExportParams{Room: room, Format: "html"}); err != nil {
		t.Fatal(err)
	} else if hb, _ := os.ReadFile(p2); !strings.Contains(string(hb), "Historial de ediciones") || !strings.Contains(string(hb), "Mensaje eliminado") {
		t.Fatal("el HTML no muestra ediciones o borrados")
	}
	t.Logf("exportación JSON y HTML verificadas (%d eventos)", doc.Get("export.events").Int())
}
