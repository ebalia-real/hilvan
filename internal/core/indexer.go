package core

import (
	"context"
	"time"
)

// La búsqueda usa el índice local (así encuentra también los mensajes cifrados), pero el historial
// viejo de cada sala recién se baja al subir en la conversación. Por eso:
//   - al buscar en una sala, primero se completa su historial;
//   - en segundo plano se va completando el de todas, de a poco (como el indexador de Element).

// fillHistory baja la historia de una sala hasta tenerla completa o hasta que pase el tiempo dado.
// Devuelve si quedó completa.
func (a *Account) fillHistory(ctx context.Context, roomID string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		r := a.store.GetRoom(roomID)
		if r == nil || r.HistoryComplete || r.Membership != "join" {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		if _, err := a.backfill(ctx, roomID); err != nil {
			return false
		}
	}
}

// historyPending cuenta las salas cuyo historial todavía no se bajó entero.
func (a *Account) historyPending() int {
	n := 0
	for _, r := range a.store.AllRooms() {
		if r.Membership == "join" && !r.HistoryComplete {
			n++
		}
	}
	return n
}

// historyIndexer completa en segundo plano la historia de todas las salas, una tanda por sala y
// con pausas (sin apurar al servidor ni gastar batería de golpe).
func (a *Account) historyIndexer() {
	a.wg.Add(1)
	defer a.wg.Done()
	wait := func(d time.Duration) bool {
		select {
		case <-a.ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	if !wait(45 * time.Second) { // que primero termine de arrancar
		return
	}
	for {
		pending := false
		for _, r := range a.store.AllRooms() {
			if a.ctx.Err() != nil {
				return
			}
			if r.Membership != "join" || r.HistoryComplete {
				continue
			}
			pending = true
			_, _ = a.backfill(a.ctx, r.ID)
			if !wait(1500 * time.Millisecond) {
				return
			}
		}
		pause := 30 * time.Second
		if !pending {
			pause = 15 * time.Minute // salas nuevas o reingresos
		}
		if !wait(pause) {
			return
		}
	}
}
