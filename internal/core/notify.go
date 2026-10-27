package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/tidwall/gjson"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type NotifyItem struct {
	Title   string
	Body    string
	Kind    string // message | invite | call | verify
	Room    string
	EventID string
	Icon    string
	Urgent  bool
}

type Notifier interface {
	Notify(a *Account, n NotifyItem)
	Close(account, room string)
}

type pushResult struct{ notify, highlight bool }

type pushRoom struct {
	a      *Account
	roomID string
}

func (r pushRoom) GetOwnDisplayname() string {
	if p := r.a.memberProfile(r.roomID, r.a.UserID()); p.Name != "" {
		return p.Name
	}
	r.a.profileMu.RLock()
	defer r.a.profileMu.RUnlock()
	return r.a.displayName
}

func (r pushRoom) GetMemberCount() int {
	if s := r.a.store.GetRoom(r.roomID); s != nil && s.Joined > 0 {
		return s.Joined
	}
	n := 0
	for _, m := range r.a.store.StateOfType(r.roomID, "m.room.member") {
		if gjson.GetBytes(m.Content, "membership").Str == "join" {
			n++
		}
	}
	return n
}

// evalPush evalúa las reglas push localmente (necesario en salas cifradas).
func (a *Account) evalPush(roomID string, row *EventRow) pushResult {
	if a.isIgnored(row.Sender) {
		return pushResult{}
	}
	rs := a.pushRules.Load()
	if rs == nil {
		dm := a.dmUser(roomID) != ""
		body := strings.ToLower(gjson.GetBytes(row.Content, "body").Str)
		mention := strings.Contains(body, strings.ToLower(a.UserID()))
		return pushResult{notify: dm || mention, highlight: mention}
	}
	evt := &event.Event{
		ID: id.EventID(row.EventID), RoomID: id.RoomID(roomID), Sender: id.UserID(row.Sender),
		Type: event.Type{Type: row.Type, Class: event.MessageEventType}, Timestamp: row.TS, StateKey: row.StateKey,
	}
	evt.Content.VeryRaw = row.Content
	_ = json.Unmarshal(row.Content, &evt.Content.Raw)
	_ = evt.Content.ParseRaw(evt.Type)
	should := rs.GetActions(pushRoom{a: a, roomID: roomID}, evt).Should()
	return pushResult{notify: should.Notify, highlight: should.Highlight}
}

func (a *Account) notifyRows(rows []*EventRow) {
	s := a.m.Settings()
	if !s.Notifications || inQuietHours(s, time.Now()) || a.isDND() {
		return
	}
	focus := a.m.Focus()
	for _, row := range rows {
		if row.Type == "invite" {
			sum := a.RoomSummary(row.RoomID)
			if sum == nil {
				continue
			}
			title := "Nueva invitación"
			body := sum.Name
			if sum.InviterName != "" {
				body = sum.InviterName + " te invitó a " + sum.Name
			}
			a.m.Notifier.Notify(a, NotifyItem{Title: title, Body: body, Kind: "invite", Room: row.RoomID})
			continue
		}
		if focus.Window && focus.Account == a.UserID() && focus.Room == row.RoomID {
			continue
		}
		sum := a.RoomSummary(row.RoomID)
		if sum == nil || sum.Notify == "mute" {
			continue
		}
		sender := a.memberProfile(row.RoomID, row.Sender).Name
		if sender == "" {
			sender = row.Sender
		}
		title := sender
		if sum.DM == "" && sum.Name != "" {
			title = sender + " · " + sum.Name
		}
		body := "Nuevo mensaje"
		if s.NotifyShowContent {
			switch {
			case row.DecryptError != "":
				body = "🔒 Mensaje cifrado"
			default:
				c := gjson.ParseBytes(row.Content)
				switch c.Get("msgtype").Str {
				case "m.image":
					body = "📷 Imagen"
				case "m.video":
					body = "🎬 Video"
				case "m.audio":
					body = "🎤 Audio"
				case "m.file":
					body = "📎 " + c.Get("body").Str
				default:
					body = truncateRunes(c.Get("body").Str, 200)
				}
				if row.Type == "m.sticker" {
					body = "Sticker"
				}
				if strings.HasSuffix(row.Type, "poll.start") {
					body = "📊 Encuesta"
				}
			}
		}
		a.m.Notifier.Notify(a, NotifyItem{Title: title, Body: body, Kind: "message", Room: row.RoomID, EventID: row.EventID})
	}
}

// --- notificaciones de escritorio por D-Bus (freedesktop) ---

type dbusNotifier struct {
	m      *Manager
	mu     sync.Mutex
	conn   *dbus.Conn
	ids    map[uint32]NotifyItem
	accts  map[uint32]string
	byRoom map[string][]uint32
	icon   string
	custom string // ícono elegido por la persona (ruta a un PNG)
	// OnActivate lo provee el shell para traer la ventana al frente.
	OnActivate func()
}

func newNotifier(m *Manager) Notifier {
	n := &dbusNotifier{m: m, ids: map[uint32]NotifyItem{}, accts: map[uint32]string{}, byRoom: map[string][]uint32{}}
	if exe, err := os.Executable(); err == nil {
		icon := filepath.Join(filepath.Dir(exe), "..", "share", "icons", "hicolor", "256x256", "apps", "io.github.heelxel.Hilvan.png")
		if _, err := os.Stat(icon); err == nil {
			n.icon = icon
		}
	}
	if n.icon == "" {
		n.icon = "io.github.heelxel.Hilvan"
	}
	go n.listen()
	return n
}

func (n *dbusNotifier) getConn() *dbus.Conn {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn != nil && n.conn.Connected() {
		return n.conn
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil
	}
	n.conn = conn
	return conn
}

func (n *dbusNotifier) listen() {
	conn := n.getConn()
	if conn == nil {
		return
	}
	_ = conn.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.Notifications"), dbus.WithMatchMember("ActionInvoked"))
	ch := make(chan *dbus.Signal, 16)
	conn.Signal(ch)
	for sig := range ch {
		if sig.Name != "org.freedesktop.Notifications.ActionInvoked" || len(sig.Body) < 2 {
			continue
		}
		nid, _ := sig.Body[0].(uint32)
		n.mu.Lock()
		item, ok := n.ids[nid]
		acct := n.accts[nid]
		n.mu.Unlock()
		if !ok {
			continue
		}
		n.m.Broadcast(Push{Ev: "open", A: acct, D: map[string]string{"room": item.Room, "event": item.EventID, "kind": item.Kind}})
		if n.m.OnActivate != nil {
			n.m.OnActivate()
		}
	}
}

func (n *dbusNotifier) Notify(a *Account, item NotifyItem) {
	conn := n.getConn()
	if conn == nil {
		return
	}
	s := n.m.Settings()
	hints := map[string]dbus.Variant{
		"desktop-entry": dbus.MakeVariant("io.github.heelxel.Hilvan"),
		"category":      dbus.MakeVariant("im.received"),
	}
	if item.Urgent || item.Kind == "call" {
		hints["urgency"] = dbus.MakeVariant(byte(2))
	}
	if s.NotifySound {
		hints["sound-name"] = dbus.MakeVariant("message-new-instant")
	} else {
		hints["suppress-sound"] = dbus.MakeVariant(true)
	}
	var replaces uint32
	n.mu.Lock()
	if ids := n.byRoom[a.UserID()+item.Room]; len(ids) > 0 && item.Kind == "message" {
		replaces = ids[len(ids)-1]
	}
	icon := n.icon
	if n.custom != "" {
		icon = n.custom
	}
	n.mu.Unlock()
	var nid uint32
	err := conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").Call(
		"org.freedesktop.Notifications.Notify", 0,
		"Hilvan", replaces, icon, item.Title, escapeMarkup(item.Body), []string{"default", "Abrir"}, hints, int32(-1),
	).Store(&nid)
	if err != nil {
		return
	}
	n.mu.Lock()
	n.ids[nid] = item
	n.accts[nid] = a.UserID()
	key := a.UserID() + item.Room
	if replaces == 0 {
		n.byRoom[key] = append(n.byRoom[key], nid)
	}
	n.mu.Unlock()
}

// Close retira las notificaciones de una sala cuando el usuario la abre.
func (n *dbusNotifier) Close(account, room string) {
	n.mu.Lock()
	ids := n.byRoom[account+room]
	delete(n.byRoom, account+room)
	n.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	conn := n.getConn()
	if conn == nil {
		return
	}
	for _, id := range ids {
		_ = conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").Call(
			"org.freedesktop.Notifications.CloseNotification", 0, id).Err
		n.mu.Lock()
		delete(n.ids, id)
		delete(n.accts, id)
		n.mu.Unlock()
	}
}

func escapeMarkup(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// setNotifyIcon usa el ícono elegido en las notificaciones ("" vuelve al clásico).
func (m *Manager) setNotifyIcon(path string) {
	if n, ok := m.Notifier.(*dbusNotifier); ok {
		n.mu.Lock()
		n.custom = path
		n.mu.Unlock()
	}
}
