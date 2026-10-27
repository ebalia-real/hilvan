package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAsE2E(t *testing.T) {
	hs := testHS(t)
	ctx := context.Background()
	ana := newTester(t, hs, "ana", "prueba-ana-123")
	room, err := ana.a.CreateRoom(ctx, CreateRoomParams{Name: "Guardar como", Encrypted: true})
	if err != nil {
		t.Fatal(err)
	}
	idOf := func(txn string) string {
		var id string
		ana.waitFor("enviado", 60*time.Second, func(p Push) bool {
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
	txn, err := ana.a.Send(ctx, &SendParams{Room: room, Text: "hola, guardame"})
	if err != nil {
		t.Fatal(err)
	}
	textID := idOf(txn)
	data := make([]byte, 3<<20)
	_, _ = rand.Read(data)
	src := filepath.Join(t.TempDir(), "envio.bin")
	_ = os.WriteFile(src, data, 0o600)
	txn, err = ana.a.SendFile(ctx, room, src, "image/png", "foto", "", nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	fileID := idOf(txn)

	// El diálogo recibe el nombre sugerido y la última carpeta; acá elegimos sin abrirlo.
	dir := t.TempDir()
	var asked []string
	ana.m.PickSavePath = func(title, name, folder string) (string, error) {
		asked = append(asked, name+"|"+folder)
		return filepath.Join(dir, "elegido-"+name), nil
	}
	dest, save, err := ana.a.SaveAs(ctx, room, fileID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dest, "elegido-foto.png") {
		t.Fatalf("nombre sugerido: %s", dest)
	}
	if err := save(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, data) {
		t.Fatal("el archivo guardado no es igual al enviado")
	}

	dest, save, err = ana.a.SaveAs(ctx, room, textID)
	if err != nil {
		t.Fatal(err)
	}
	if err := save(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !strings.Contains(string(got), "hola, guardame") || !strings.HasSuffix(dest, ".txt") {
		t.Fatalf("texto guardado: %s %q", dest, got)
	}
	// La segunda vez propone la carpeta elegida antes.
	if len(asked) != 2 || !strings.HasSuffix(asked[1], "|"+dir) {
		t.Fatalf("carpetas propuestas: %v", asked)
	}

	// Cancelar no guarda nada.
	ana.m.PickSavePath = func(title, name, folder string) (string, error) { return "", nil }
	if _, _, err := ana.a.SaveAs(ctx, room, fileID); err != ErrCanceled {
		t.Fatalf("cancelar: %v", err)
	}
}
