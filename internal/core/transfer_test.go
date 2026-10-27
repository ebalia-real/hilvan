package core

// Archivos grandes: más grandes que el tope del servidor, cifrados, verificados byte a byte.
// HILVAN_TEST_HS=http://localhost:8008 go test -tags goolm,sqlite_fts5 ./internal/core -run BigFile -v

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestBigFileE2E(t *testing.T) {
	hs := testHS(t)
	ctx := context.Background()
	ana := newTester(t, hs, "ana", "prueba-ana-123")
	beto := newTester(t, hs, "beto", "prueba-beto-123")
	if _, err := ana.a.SetupRecovery(ctx, "prueba-ana-123", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := beto.a.SetupRecovery(ctx, "prueba-beto-123", ""); err != nil {
		t.Fatal(err)
	}
	limit := ana.a.uploadLimit(ctx)
	t.Logf("tope de subida del servidor: %s", humanSize(limit))
	size := int64(300 << 20)
	if limit > 0 && size > limit {
		size = limit
	}
	// Lo que no entra en el servidor se avisa antes de subir (con un mensaje que explica de quién es el límite).
	if limit > 0 {
		if err := ana.a.CheckUploadSize(ctx, limit+1); err == nil || !strings.Contains(err.Error(), "límite del servidor") {
			t.Fatalf("no avisó del tope del servidor: %v", err)
		}
	}
	src := filepath.Join(t.TempDir(), "grande.bin")
	f, _ := os.Create(src)
	h := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(f, h), rand.Reader, size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	want := h.Sum(nil)

	betoID := beto.a.UserID()
	room, err := ana.a.CreateRoom(ctx, CreateRoomParams{Direct: true, Encrypted: true, Invite: []string{betoID}})
	if err != nil {
		t.Fatal(err)
	}
	beto.waitFor("invitación", 30*time.Second, func(p Push) bool {
		for _, r := range roomsIn(p) {
			if r.ID == room && r.Membership == "invite" {
				return true
			}
		}
		return false
	})
	if _, err := beto.a.Join(ctx, room, nil, ""); err != nil {
		t.Fatal(err)
	}
	ana.waitFor("beto se une", 30*time.Second, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if it.Type == "m.room.member" && it.SK != nil && *it.SK == betoID {
				return true
			}
		}
		return false
	})
	start := time.Now()
	if _, err := ana.a.SendFile(ctx, room, src, "application/octet-stream", "grande.bin", "", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	var evID string
	beto.waitFor("archivo recibido", 5*time.Minute, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if it.UTD == "" && gjson.GetBytes(it.Content, "filename").Str == "grande.bin" {
				evID = it.ID
				return true
			}
		}
		return false
	})
	content := gjson.GetBytes(beto.a.store.GetEvent(evID).Content, "@this")
	t.Logf("enviado %s en %v (msgtype %s)", humanSize(size), time.Since(start).Round(time.Millisecond), content.Get("msgtype").Str)
	if content.Get("msgtype").Str != "m.file" || !content.Get("file.url").Exists() || content.Get(`io\.hilvan\.parts`).Exists() {
		t.Fatalf("se esperaba un único archivo Matrix normal (cifrado), llegó: %s", content.Raw[:min(300, len(content.Raw))])
	}
	start = time.Now()
	res, err := beto.a.FileOf(ctx, room, evID)
	if err != nil {
		t.Fatalf("descarga: %v", err)
	}
	got, err := fileSHA256(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("el archivo descargado NO es idéntico al original")
	}
	st, _ := os.Stat(res.Path)
	t.Logf("descargado y verificado (%s, SHA-256 idéntico) en %v", strconv.FormatInt(st.Size(), 10)+" bytes", time.Since(start).Round(time.Millisecond))

	// Una parte manipulada tiene que detectarse, nunca entregarse como válida.
	os.Remove(res.Path)
	bad := fileRef{File: nil, Size: 3, SHA256: "AAAA"}
	tmp := filepath.Join(t.TempDir(), "x.raw")
	_ = os.WriteFile(tmp, []byte("abc"), 0o600)
	if _, err := copyVerified(io.Discard, tmp, bad, sha256.New()); err == nil {
		t.Fatal("una parte con hash incorrecto pasó la verificación")
	}
}
