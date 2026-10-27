package core

import "time"

// Retención (m.room.retention): además de lo que borre el servidor, Hilvan deja de guardar y de
// mostrar los mensajes vencidos en este equipo.

func (a *Account) retentionLoop() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	a.applyRetention()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
			a.applyRetention()
		}
	}
}

func (a *Account) applyRetention() {
	now := time.Now().UnixMilli()
	var total int64
	for _, s := range a.RoomList() {
		if s.Retention > 0 && s.Membership == "join" {
			total += a.store.PurgeBefore(s.ID, now-s.Retention)
		}
	}
	if total > 0 {
		a.log.Info().Int64("mensajes", total).Msg("Retención: mensajes vencidos borrados de este equipo")
	}
}
