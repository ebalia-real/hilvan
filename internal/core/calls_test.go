package core

// Llamada 1:1 real entre dos cuentas con el motor nativo (fuentes de prueba de GStreamer).
// HILVAN_TEST_HS=http://localhost:8008 go test -tags goolm,sqlite_fts5 ./internal/core -run CallE2E -v

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"hilvan/internal/media"
)

func callIn(p Push) *CallView {
	if p.Ev != "call" {
		return nil
	}
	b, _ := json.Marshal(p.D)
	var v CallView
	_ = json.Unmarshal(b, &v)
	return &v
}

func TestCallE2E(t *testing.T) {
	hs := testHS(t)
	if !media.Available() {
		t.Skip("sin GStreamer")
	}
	os.Setenv("HILVAN_CALL_TEST_MEDIA", "1")
	ctx := context.Background()
	ana := newTester(t, hs, "ana", "prueba-ana-123")
	beto := newTester(t, hs, "beto", "prueba-beto-123")
	for _, tt := range []*tester{ana, beto} {
		tt.m.Calls.EnableNativeCalls(true)
	}
	if _, err := ana.a.SetupRecovery(ctx, "prueba-ana-123", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := beto.a.SetupRecovery(ctx, "prueba-beto-123", ""); err != nil {
		t.Fatal(err)
	}
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
			if it.Type == "m.room.member" && it.SK != nil && *it.SK == betoID && gjson.GetBytes(it.Content, "membership").Str == "join" {
				return true
			}
		}
		return false
	})

	start := time.Now()
	c, err := ana.m.Calls.Place(ana.a, room, true, false, false)
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	ring := beto.waitFor("llamada entrante", 30*time.Second, func(p Push) bool {
		v := callIn(p)
		return v != nil && v.CallID == c.ID && v.State == "ringing"
	})
	rv := callIn(ring)
	if !rv.Video || rv.Outgoing {
		t.Fatalf("invitación inesperada: %+v", rv)
	}
	t.Logf("timbre a los %v", time.Since(start))
	if err := beto.m.Calls.Answer(rv.ID, true, false, false); err != nil {
		t.Fatalf("answer: %v", err)
	}
	for _, tt := range []*tester{ana, beto} {
		tt.waitFor("conectado", 30*time.Second, func(p Push) bool {
			v := callIn(p)
			return v != nil && v.CallID == c.ID && v.State == "connected"
		})
	}
	t.Logf("conectado a los %v", time.Since(start))
	// Video remoto y audio que llega del otro lado (el tono de prueba supera el umbral de voz).
	for _, tt := range []*tester{ana, beto} {
		tt.waitFor("video remoto", 20*time.Second, func(p Push) bool {
			v := callIn(p)
			if v == nil || v.CallID != c.ID {
				return false
			}
			for _, s := range v.Streams {
				if s == "remote" {
					return true
				}
			}
			return false
		})
		tt.waitFor("audio remoto", 20*time.Second, func(p Push) bool {
			if p.Ev != "call.speaking" {
				return false
			}
			d, _ := p.D.(map[string]any)
			return d["who"] == "remote" && d["speaking"] == true
		})
	}
	stats, _ := json.Marshal(beto.m.Calls.get(rv.ID).engine.Stats())
	t.Logf("estadísticas: %s", stats)
	// Silenciar: el otro lado deja de oír.
	ana.m.Calls.SetMuted(c.key, true)
	beto.waitFor("silencio remoto", 10*time.Second, func(p Push) bool {
		if p.Ev != "call.speaking" {
			return false
		}
		d, _ := p.D.(map[string]any)
		return d["who"] == "remote" && d["speaking"] == false
	})
	if err := ana.m.Calls.Hangup(c.key, ""); err != nil {
		t.Fatal(err)
	}
	end := beto.waitFor("colgado", 30*time.Second, func(p Push) bool {
		v := callIn(p)
		return v != nil && v.CallID == c.ID && v.State == "ended"
	})
	if r := callIn(end).Reason; r != "remote_hangup" {
		t.Errorf("motivo inesperado: %s", r)
	}
}

// TestCallPeer es un interlocutor automático para probar la interfaz a mano:
// crea un DM con ana, contesta solo las llamadas y, con HILVAN_CALL_PEER=call, llama primero.
// HILVAN_CALL_PEER=answer|call HILVAN_TEST_HS=... go test ... -run CallPeer -timeout 20m
func TestCallPeer(t *testing.T) {
	mode := os.Getenv("HILVAN_CALL_PEER")
	if mode == "" {
		t.Skip("HILVAN_CALL_PEER no definido")
	}
	hs := testHS(t)
	os.Setenv("HILVAN_CALL_TEST_MEDIA", "1")
	ctx := context.Background()
	beto := newTester(t, hs, "beto", "prueba-beto-123")
	beto.m.Calls.EnableNativeCalls(true)
	if _, err := beto.a.SetupRecovery(ctx, "prueba-beto-123", ""); err != nil {
		t.Fatal(err)
	}
	anaID := "@ana:" + strings.SplitN(beto.a.UserID(), ":", 2)[1]
	room, err := beto.a.CreateRoom(ctx, CreateRoomParams{Direct: true, Name: "Prueba de llamadas", Invite: []string{anaID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sala %s: esperando a ana", room)
	beto.waitFor("ana se une", 10*time.Minute, func(p Push) bool {
		for _, it := range itemsIn(p, room) {
			if it.Type == "m.room.member" && it.SK != nil && *it.SK == anaID && gjson.GetBytes(it.Content, "membership").Str == "join" {
				return true
			}
		}
		return false
	})
	t.Log("ana entró")
	if mode == "call" {
		time.Sleep(4 * time.Second)
		if _, err := beto.m.Calls.Place(beto.a, room, true, false, false); err != nil {
			t.Fatal(err)
		}
		t.Log("llamando a ana")
	}
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		p := beto.waitFor("evento", time.Until(deadline), func(p Push) bool { return p.Ev == "call" })
		beto.seen = nil
		v := callIn(p)
		t.Logf("llamada %s: %s %s", v.ID, v.State, v.Reason)
		if v.State == "ringing" {
			time.Sleep(1500 * time.Millisecond)
			if err := beto.m.Calls.Answer(v.ID, true, false, false); err != nil {
				t.Logf("answer: %v", err)
			}
		}
	}
}

// TestCallScreenE2E: llamada de voz → transmitir un monitor (captura directa de GNOME) → el otro lado
// recibe el video tras la renegociación. HILVAN_TEST_SCREENS=1 además de HILVAN_TEST_HS.
func TestCallScreenE2E(t *testing.T) {
	hs := testHS(t)
	if os.Getenv("HILVAN_TEST_SCREENS") == "" {
		t.Skip("HILVAN_TEST_SCREENS no definido")
	}
	mons, err := listMonitors()
	if err != nil || len(mons) == 0 {
		t.Skip("sin monitores")
	}
	os.Setenv("HILVAN_CALL_TEST_MEDIA", "1")
	ctx := context.Background()
	ana := newTester(t, hs, "ana", "prueba-ana-123")
	beto := newTester(t, hs, "beto", "prueba-beto-123")
	// ana como en la ventana (sin modo headless), beto headless.
	ana.m.Calls.EnableNativeCalls(os.Getenv("HILVAN_TEST_HEADLESS") == "1")
	beto.m.Calls.EnableNativeCalls(true)
	betoID := beto.a.UserID()
	room, err := ana.a.CreateRoom(ctx, CreateRoomParams{Direct: true, Invite: []string{betoID}})
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
	c, err := ana.m.Calls.Place(ana.a, room, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	ring := beto.waitFor("timbre", 30*time.Second, func(p Push) bool { v := callIn(p); return v != nil && v.CallID == c.ID && v.State == "ringing" })
	if err := beto.m.Calls.Answer(callIn(ring).ID, false, false, false); err != nil {
		t.Fatal(err)
	}
	ana.waitFor("conectado", 30*time.Second, func(p Push) bool { v := callIn(p); return v != nil && v.CallID == c.ID && v.State == "connected" })
	t0 := time.Now()
	if err := ana.m.Calls.Screen(c.key, true, ScreenOptions{Source: mons[len(mons)-1].ID, Res: "720", FPS: 15, Mbps: 2}); err != nil {
		t.Fatalf("screen: %v", err)
	}
	beto.waitFor("video de la pantalla", 30*time.Second, func(p Push) bool {
		v := callIn(p)
		if v == nil || v.CallID != c.ID {
			return false
		}
		for _, s := range v.Streams {
			if s == "remote" {
				return true
			}
		}
		return false
	})
	t.Logf("beto recibe la pantalla a los %v", time.Since(t0))
	if err := ana.m.Calls.Screen(c.key, false, ScreenOptions{}); err != nil {
		t.Fatal(err)
	}
	_ = ana.m.Calls.Hangup(c.key, "")
}
