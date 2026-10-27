package core

// Pruebas de extremo a extremo contra un servidor real.
// Ejecutar con: HILVAN_TEST_HS=http://localhost:8008 go test -tags goolm,sqlite_fts5 ./internal/core -run E2E -v

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

type tester struct {
	t    *testing.T
	name string
	m    *Manager
	a    *Account
	ch   <-chan Push
	seen []Push
}

func testHS(t *testing.T) string {
	hs := os.Getenv("HILVAN_TEST_HS")
	if hs == "" {
		t.Skip("HILVAN_TEST_HS no definido")
	}
	return hs
}

func newTester(t *testing.T, hs, user, pass string) *tester {
	t.Helper()
	os.Setenv("HILVAN_NO_KEYRING", "1")
	dir := t.TempDir()
	log := zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) { w.Out = os.Stderr })).Level(zerolog.WarnLevel)
	if os.Getenv("HILVAN_TEST_DEBUG") != "" {
		log = log.Level(zerolog.DebugLevel)
	}
	m, err := NewManager(DefaultPaths(dir), "test", log.With().Str("t", user).Logger())
	if err != nil {
		t.Fatal(err)
	}
	m.Notifier = nopNotifier{}
	_, ch := m.Subscribe()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	info, err := m.LoginPassword(ctx, hs, user, pass)
	if err != nil {
		t.Fatalf("login %s: %v", user, err)
	}
	a, _ := m.Account(info.UserID)
	tt := &tester{t: t, name: user, m: m, a: a, ch: ch}
	tt.waitFor("account.ready", 60*time.Second, func(p Push) bool { return p.Ev == "account.ready" })
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Logout(c, info.UserID)
		m.Stop()
	})
	return tt
}

type nopNotifier struct{}

func (nopNotifier) Notify(*Account, NotifyItem) {}
func (nopNotifier) Close(string, string)        {}

func (tt *tester) waitFor(what string, timeout time.Duration, pred func(Push) bool) Push {
	tt.t.Helper()
	for _, p := range tt.seen {
		if pred(p) {
			return p
		}
	}
	deadline := time.After(timeout)
	for {
		select {
		case p := <-tt.ch:
			tt.seen = append(tt.seen, p)
			if len(tt.seen) > 5000 {
				tt.seen = tt.seen[1000:]
			}
			if pred(p) {
				return p
			}
		case <-deadline:
			tt.t.Fatalf("[%s] tiempo agotado esperando %s", tt.name, what)
		}
	}
}

// itemsIn devuelve los elementos de un push "sync" para una sala.
func itemsIn(p Push, room string) []Item {
	if p.Ev != "sync" {
		return nil
	}
	b, _ := json.Marshal(p.D)
	var d struct {
		Timeline map[string]struct {
			New     []Item `json:"new"`
			Updated []Item `json:"updated"`
		} `json:"timeline"`
	}
	_ = json.Unmarshal(b, &d)
	t := d.Timeline[room]
	return append(t.New, t.Updated...)
}

func roomsIn(p Push) []RoomSummary {
	if p.Ev != "sync" {
		return nil
	}
	b, _ := json.Marshal(p.D)
	var d struct {
		Rooms []RoomSummary `json:"rooms"`
	}
	_ = json.Unmarshal(b, &d)
	return d.Rooms
}

func body(it Item) string { return gjson.GetBytes(it.Content, "body").Str }

func TestE2E(t *testing.T) {
	hs := testHS(t)
	ctx := context.Background()
	ana := newTester(t, hs, "ana", "prueba-ana-123")
	beto := newTester(t, hs, "beto", "prueba-beto-123")
	anaID, betoID := ana.a.UserID(), beto.a.UserID()

	// 1. Identidad criptográfica + clave de recuperación.
	anaKey, err := ana.a.SetupRecovery(ctx, "prueba-ana-123", "")
	if err != nil {
		t.Fatalf("setup ana: %v", err)
	}
	betoKey, err := beto.a.SetupRecovery(ctx, "prueba-beto-123", "")
	if err != nil {
		t.Fatalf("setup beto: %v", err)
	}
	if st := ana.a.CryptoStatus(ctx); !st.Verified || !st.CrossSigning || st.Backup == nil || !st.BackupKey {
		t.Fatalf("estado cripto de ana incorrecto tras setup: %+v", st)
	}
	_ = anaKey

	// 2. DM cifrado.
	room, err := ana.a.CreateRoom(ctx, CreateRoomParams{Direct: true, Encrypted: true, Invite: []string{betoID}})
	if err != nil {
		t.Fatalf("dm: %v", err)
	}
	beto.waitFor("invitación", 30*time.Second, func(p Push) bool {
		for _, r := range roomsIn(p) {
			if r.ID == room && r.Membership == "invite" {
				return true
			}
		}
		return false
	})
	if _, err = beto.a.Join(ctx, room, nil, ""); err != nil {
		t.Fatalf("join: %v", err)
	}
	ana.waitFor("beto se une", 30*time.Second, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if it.Type == "m.room.member" && it.SK != nil && *it.SK == betoID && gjson.GetBytes(it.Content, "membership").Str == "join" {
				return true
			}
		}
		return false
	})

	// 3. Mensaje cifrado con eco local.
	txn, err := ana.a.Send(ctx, &SendParams{Room: room, Text: "hola **beto**", Markdown: true})
	if err != nil {
		t.Fatal(err)
	}
	ana.waitFor("eco local", 5*time.Second, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if it.Txn == txn && it.Status == "sending" {
				return true
			}
		}
		return false
	})
	var msgID string
	beto.waitFor("mensaje descifrado", 30*time.Second, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if strings.HasPrefix(body(it), "hola") && it.UTD == "" && it.Enc {
				msgID = it.ID
				if gjson.GetBytes(it.Content, "formatted_body").Str != "hola <strong>beto</strong>" {
					t.Errorf("markdown inesperado: %s", it.Content)
				}
				if it.Trust != "tofu" && it.Trust != "verified" {
					t.Errorf("confianza inesperada: %s", it.Trust)
				}
				return true
			}
		}
		return false
	})
	ana.waitFor("eco remoto reemplaza al local", 30*time.Second, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if it.Txn == txn && it.ID == msgID && it.Status == "" {
				return true
			}
		}
		return false
	})

	// 4. Reacción (cifrada) y edición.
	if _, err = beto.a.React(ctx, room, msgID, "👍"); err != nil {
		t.Fatal(err)
	}
	ana.waitFor("reacción", 30*time.Second, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if it.ID == msgID && len(it.Reactions) == 1 && it.Reactions[0].Key == "👍" && it.Reactions[0].Count == 1 {
				return true
			}
		}
		return false
	})
	if _, err = ana.a.Send(ctx, &SendParams{Room: room, Text: "hola beto (editado)", Edit: msgID}); err != nil {
		t.Fatal(err)
	}
	beto.waitFor("edición", 30*time.Second, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if it.ID == msgID && body(it) == "hola beto (editado)" && it.Edited > 0 && len(it.Reactions) == 1 {
				return true
			}
		}
		return false
	})

	// 5. Hilo.
	if _, err = beto.a.Send(ctx, &SendParams{Room: room, Text: "respuesta en hilo", Thread: msgID}); err != nil {
		t.Fatal(err)
	}
	ana.waitFor("resumen de hilo", 30*time.Second, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if it.ID == msgID && it.Thread != nil && it.Thread.Count == 1 {
				return true
			}
		}
		return false
	})
	th, err := ana.a.Thread(ctx, room, msgID)
	if err != nil || len(th.Items) != 2 || body(th.Items[1]) != "respuesta en hilo" {
		t.Fatalf("hilo incorrecto: %v %+v", err, th)
	}

	// 6. Búsqueda local en contenido cifrado.
	hits, err := beto.a.store.Search("editado", room, 10)
	if err != nil || len(hits) != 1 || hits[0].EventID != msgID {
		t.Fatalf("búsqueda local: %v %+v", err, hits)
	}

	// 7. Historia larga + paginación sin duplicados ni desorden.
	const n = 130
	for i := 0; i < n; i++ {
		if _, err = ana.a.Send(ctx, &SendParams{Room: room, Text: fmt.Sprintf("mensaje %03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	beto.waitFor("último mensaje", 60*time.Second, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if body(it) == fmt.Sprintf("mensaje %03d", n-1) {
				return true
			}
		}
		return false
	})

	// Esperamos a que el respaldo de beto tenga las claves (se suben en segundo plano).
	for i := 0; i < 50; i++ {
		v, err := beto.a.cli.GetKeyBackupLatestVersion(ctx)
		if err == nil && v.Count > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 8. Nuevo dispositivo de beto: pagina la historia y la descifra con la clave de recuperación.
	beto2 := newTester(t, hs, "beto", "prueba-beto-123")
	if st := beto2.a.CryptoStatus(ctx); st.Verified || st.Action != "verify" {
		t.Fatalf("un dispositivo nuevo no debería estar verificado: %+v", st)
	}
	res, err := beto2.a.RestoreWithRecoveryKey(ctx, betoKey)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	t.Logf("claves importadas del respaldo: %v", res)
	if st := beto2.a.CryptoStatus(ctx); !st.Verified || !st.BackupKey {
		t.Fatalf("tras restaurar debería estar verificado: %+v", st)
	}
	seen := map[string]bool{}
	var all []Item
	before := int64(0)
	for page := 0; page < 20; page++ {
		pg, err := beto2.a.Timeline(ctx, room, before, 40)
		if err != nil {
			t.Fatalf("timeline: %v", err)
		}
		for i := len(pg.Items) - 1; i >= 0; i-- {
			it := pg.Items[i]
			if seen[it.ID] {
				t.Fatalf("evento duplicado en paginación: %s", it.ID)
			}
			seen[it.ID] = true
			all = append(all, it)
		}
		if len(pg.Items) == 0 || !pg.HasMore {
			break
		}
		before = pg.Items[0].Sort
	}
	var msgs []string
	utd := 0
	for i := len(all) - 1; i >= 0; i-- {
		b := body(all[i])
		if all[i].UTD != "" {
			utd++
		}
		if strings.HasPrefix(b, "mensaje ") {
			msgs = append(msgs, b)
		}
	}
	// Los UTD se reintentan en segundo plano tras restaurar; los contamos después.
	if len(msgs)+utd < n {
		t.Fatalf("faltan mensajes en la paginación: %d descifrados + %d UTD < %d", len(msgs), utd, n)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i] < msgs[i-1] {
			t.Fatalf("orden incorrecto: %s antes de %s", msgs[i-1], msgs[i])
		}
	}
	left := 0
	for i := 0; i < 60; i++ {
		left = 0
		for _, r := range beto2.a.store.AllUTDs(5000) {
			if r.RoomID == room { // salas de corridas anteriores usan respaldos ya reemplazados
				left++
			}
		}
		if left == 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if left > 0 {
		t.Fatalf("quedaron %d mensajes sin descifrar tras restaurar el respaldo", left)
	}

	// 9. Verificación por emojis entre dos dispositivos de beto.
	beto3 := newTester(t, hs, "beto", "prueba-beto-123")
	txnID, err := beto3.a.StartVerification(ctx, betoID, "")
	if err != nil {
		t.Fatalf("verify start: %v", err)
	}
	req := beto.waitFor("solicitud de verificación", 30*time.Second, func(p Push) bool {
		v, ok := p.D.(verifyState)
		return p.Ev == "verify" && ok && v.Phase == "requested" && v.Incoming
	})
	inTxn := req.D.(verifyState).TxnID
	if inTxn != txnID {
		t.Fatalf("txn distinto: %s vs %s", inTxn, txnID)
	}
	if err = beto.a.VerifyAction(ctx, inTxn, "accept"); err != nil {
		t.Fatal(err)
	}
	sasA := beto.waitFor("SAS en beto", 30*time.Second, func(p Push) bool {
		v, ok := p.D.(verifyState)
		return p.Ev == "verify" && ok && v.Phase == "sas"
	}).D.(verifyState)
	sasB := beto3.waitFor("SAS en beto3", 30*time.Second, func(p Push) bool {
		v, ok := p.D.(verifyState)
		return p.Ev == "verify" && ok && v.Phase == "sas"
	}).D.(verifyState)
	if strings.Join(sasA.Emojis, "") != strings.Join(sasB.Emojis, "") || len(sasA.Emojis) != 7 {
		t.Fatalf("los emojis no coinciden: %v vs %v", sasA.Emojis, sasB.Emojis)
	}
	_ = beto.a.VerifyAction(ctx, inTxn, "confirm")
	_ = beto3.a.VerifyAction(ctx, txnID, "confirm")
	beto3.waitFor("verificación completa", 30*time.Second, func(p Push) bool {
		v, ok := p.D.(verifyState)
		return p.Ev == "verify" && ok && v.Phase == "done"
	})
	// Tiene que quedar verificado enseguida (sin esperar secretos de a uno ni el respaldo completo).
	doneAt := time.Now()
	ok := false
	for i := 0; i < 60 && !ok; i++ {
		ok = beto3.a.CryptoStatus(ctx).Verified
		if !ok {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !ok {
		t.Fatalf("beto3 debería quedar verificado tras la verificación por emojis: %+v", beto3.a.CryptoStatus(ctx))
	}
	took := time.Since(doneAt)
	t.Logf("verificado %v después de confirmar los emojis", took.Round(time.Millisecond))
	if took > 3*time.Second {
		t.Errorf("tardó demasiado en quedar verificado: %v", took)
	}

	// 10. Redacción.
	if err = ana.a.Redact(ctx, room, msgID, ""); err != nil {
		t.Fatal(err)
	}
	beto.waitFor("redacción", 30*time.Second, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if it.ID == msgID && it.Redacted {
				return true
			}
		}
		return false
	})
	if hits, _ := beto.a.store.Search("editado", room, 10); len(hits) != 0 {
		t.Fatalf("un mensaje borrado no debería aparecer en la búsqueda")
	}
	_ = anaID
}
