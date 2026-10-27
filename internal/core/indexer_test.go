package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Un mensaje viejo que este dispositivo nunca bajó tiene que aparecer al buscar en la sala,
// sin tener que subir hasta arriba en la conversación.
func TestSearchOldHistoryE2E(t *testing.T) {
	hs := testHS(t)
	ctx := context.Background()
	beto := newTester(t, hs, "beto", "prueba-beto-123")
	room, err := beto.a.CreateRoom(ctx, CreateRoomParams{Name: "Historia larga", Invite: []string{"@ana:localhost"}})
	if err != nil {
		t.Fatal(err)
	}
	send := func(body string) {
		if _, err := beto.a.cli.SendMessageEvent(ctx, id.RoomID(room), event.EventMessage,
			&event.MessageEventContent{MsgType: event.MsgText, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	send("la aguja perdida en el pajar")
	for i := 0; i < 250; i++ {
		send(fmt.Sprintf("paja número %d", i))
	}

	ana := newTester(t, hs, "ana", "prueba-ana-123")
	if _, err := ana.a.Join(ctx, room, nil, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if r := ana.a.store.GetRoom(room); r != nil && r.Membership == "join" && len(ana.a.store.TimelineBefore(room, 0, 10)) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if hits, _ := ana.a.store.Search("aguja", room, 10); len(hits) != 0 {
		t.Skip("el servidor mandó toda la historia de una; no hay nada que completar")
	}
	r0 := ana.a.store.GetRoom(room)
	t.Logf("antes: prev_batch=%q completa=%v eventos=%d", r0.PrevBatch, r0.HistoryComplete, len(ana.a.store.TimelineBefore(room, 0, 1000)))
	start := time.Now()
	if !ana.a.fillHistory(ctx, room, 30*time.Second) {
		t.Fatal("no terminó de bajar la historia")
	}
	r1 := ana.a.store.GetRoom(room)
	t.Logf("después: prev_batch=%q completa=%v eventos=%d", r1.PrevBatch, r1.HistoryComplete, len(ana.a.store.TimelineBefore(room, 0, 1000)))
	hits, err := ana.a.store.Search("aguja", room, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("no encontró el mensaje viejo: %v %v", hits, err)
	}
	t.Logf("historia completa y mensaje encontrado en %v", time.Since(start))
}
