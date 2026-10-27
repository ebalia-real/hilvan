package core

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
)

// Estado del usuario al estilo Discord: en línea, ausente, no molestar, invisible,
// más un mensaje personalizado con vencimiento opcional.
type UserStatus struct {
	Status  string `json:"status"`            // online | away | dnd | invisible
	Message string `json:"message,omitempty"` // texto libre (puede incluir emoji)
	Until   int64  `json:"until,omitempty"`   // ms; 0 = no vence
}

func (a *Account) loadStatus() UserStatus {
	var st UserStatus
	_ = json.Unmarshal([]byte(a.store.GetKV("user_status")), &st)
	if st.Status == "" {
		st.Status = "online"
	}
	if st.Until > 0 && time.Now().UnixMilli() > st.Until {
		st.Message, st.Until = "", 0
	}
	return st
}

// presenceFor traduce el estado a presencia Matrix.
func presenceFor(st UserStatus, showPresence bool) event.Presence {
	if !showPresence {
		return event.PresenceOffline
	}
	switch st.Status {
	case "away", "dnd":
		return event.PresenceUnavailable
	case "invisible":
		return event.PresenceOffline
	}
	return event.PresenceOnline
}

// statusMsgFor agrega el marcador de "no molestar" para que otros clientes lo vean.
func statusMsgFor(st UserStatus) string {
	msg := strings.TrimSpace(st.Message)
	if st.Status == "dnd" && !strings.HasPrefix(msg, "⛔") {
		if msg == "" {
			return "⛔ No molestar"
		}
		return "⛔ " + msg
	}
	return msg
}

func (a *Account) applyStatus(ctx context.Context) error {
	st := a.loadStatus()
	pres := presenceFor(st, a.m.Settings().ShowPresence)
	a.cli.SyncPresence = pres
	if st.Status == "invisible" {
		return a.cli.SetPresence(ctx, mautrix.ReqPresence{Presence: event.PresenceOffline})
	}
	return a.cli.SetPresence(ctx, mautrix.ReqPresence{Presence: pres, StatusMsg: statusMsgFor(st)})
}

func (a *Account) SetStatus(ctx context.Context, st UserStatus) error {
	switch st.Status {
	case "online", "away", "dnd", "invisible":
	default:
		st.Status = "online"
	}
	b, _ := json.Marshal(st)
	a.store.SetKV("user_status", string(b))
	err := a.applyStatus(ctx)
	a.m.Broadcast(Push{Ev: "accounts", D: a.m.AccountsInfo()})
	return friendlyErrorNil(err)
}

// statusLoop vence los mensajes de estado temporales.
func (a *Account) statusLoop() {
	a.wg.Add(1)
	defer a.wg.Done()
	_ = a.applyStatus(a.ctx)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
			var st UserStatus
			_ = json.Unmarshal([]byte(a.store.GetKV("user_status")), &st)
			if st.Until > 0 && time.Now().UnixMilli() > st.Until {
				st.Message, st.Until = "", 0
				_ = a.SetStatus(a.ctx, st)
			}
		}
	}
}

func (a *Account) isDND() bool { return a.loadStatus().Status == "dnd" }
