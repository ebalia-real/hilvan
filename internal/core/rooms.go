package core

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"maunium.net/go/mautrix/pushrules"
)

type Profile struct {
	Name   string `json:"name,omitempty"`
	Avatar string `json:"avatar,omitempty"`
}

type Hero struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Avatar string `json:"avatar,omitempty"`
}

type SpaceChild struct {
	ID        string   `json:"id"`
	Order     string   `json:"order,omitempty"`
	Suggested bool     `json:"suggested,omitempty"`
	Via       []string `json:"via,omitempty"`
}

type Bridge struct {
	Protocol string `json:"protocol"`
	Name     string `json:"name,omitempty"`
	Avatar   string `json:"avatar,omitempty"`
	Channel  string `json:"channel,omitempty"`
}

type Preview struct {
	Sender  string `json:"sender"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type"`
	MsgType string `json:"msgtype,omitempty"`
	Body    string `json:"body,omitempty"`
	UTD     bool   `json:"utd,omitempty"`
}

type RoomSummary struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Avatar       string       `json:"avatar,omitempty"`
	Topic        string       `json:"topic,omitempty"`
	Alias        string       `json:"alias,omitempty"`
	Type         string       `json:"type,omitempty"`
	Membership   string       `json:"membership"`
	Encrypted    bool         `json:"encrypted,omitempty"`
	DM           string       `json:"dm,omitempty"`
	Heroes       []Hero       `json:"heroes,omitempty"`
	Tags         []string     `json:"tags,omitempty"`
	Unread       int          `json:"unread"`
	Highlight    int          `json:"highlight"`
	MarkedUnread bool         `json:"marked_unread,omitempty"`
	Notify       string       `json:"notify"`
	LastTS       int64        `json:"last_ts"`
	Preview      *Preview     `json:"preview,omitempty"`
	Joined       int          `json:"joined"`
	Invited      int          `json:"invited,omitempty"`
	Parents      []string     `json:"parents,omitempty"`
	Children     []SpaceChild `json:"children,omitempty"`
	Inviter      string       `json:"inviter,omitempty"`
	InviterName  string       `json:"inviter_name,omitempty"`
	Bridge       *Bridge      `json:"bridge,omitempty"`
	Tombstone    string       `json:"tombstone,omitempty"`
	Predecessor  string       `json:"predecessor,omitempty"`
	JoinRule     string       `json:"join_rule,omitempty"`
	HistoryVis   string       `json:"history_visibility,omitempty"`
	Call         []string     `json:"call,omitempty"`
	Widgets      int          `json:"widgets,omitempty"`
	Power        int          `json:"power"`
	Version      string       `json:"version,omitempty"`
	Pinned       int          `json:"pinned,omitempty"`
	Retention    int64        `json:"retention_ms,omitempty"` // m.room.retention: los mensajes más viejos se borran
	LocalOnly    bool         `json:"local_only,omitempty"`   // sala sin federación (m.federate=false)
	ServerACL    bool         `json:"server_acl,omitempty"`
	HasMore      bool         `json:"has_more"`
}

func (a *Account) memberProfile(roomID, userID string) Profile {
	var c struct {
		Name   string `json:"displayname"`
		Avatar string `json:"avatar_url"`
	}
	if a.store.GetStateContent(roomID, "m.room.member", userID, &c) {
		return Profile{Name: c.Name, Avatar: c.Avatar}
	}
	return Profile{}
}

func (a *Account) RoomSummary(roomID string) *RoomSummary {
	r := a.store.GetRoom(roomID)
	if r == nil {
		return nil
	}
	me := a.UserID()
	s := &RoomSummary{ID: roomID, Membership: r.Membership, Joined: r.Joined, Invited: r.Invited,
		MarkedUnread: r.MarkedUnread, LastTS: r.BumpTS, HasMore: !r.HistoryComplete}

	str := func(typ, path string) string {
		e := a.store.GetState(roomID, typ, "")
		if e == nil {
			return ""
		}
		return gjson.GetBytes(e.Content, path).Str
	}
	s.Name = strings.TrimSpace(str("m.room.name", "name"))
	s.Topic = str("m.room.topic", "topic")
	s.Avatar = str("m.room.avatar", "url")
	s.Alias = str("m.room.canonical_alias", "alias")
	s.JoinRule = str("m.room.join_rules", "join_rule")
	s.HistoryVis = str("m.room.history_visibility", "history_visibility")
	s.Tombstone = str("m.room.tombstone", "replacement_room")
	if create := a.store.GetState(roomID, "m.room.create", ""); create != nil {
		c := gjson.ParseBytes(create.Content)
		s.Type = c.Get("type").Str
		s.Version = c.Get("room_version").Str
		s.Predecessor = c.Get(`predecessor.room_id`).Str
		if f := c.Get(`m\.federate`); f.Exists() && !f.Bool() {
			s.LocalOnly = true
		}
	}
	if e := a.store.GetState(roomID, "m.room.retention", ""); e != nil {
		s.Retention = gjson.GetBytes(e.Content, "max_lifetime").Int()
	}
	s.ServerACL = a.store.GetState(roomID, "m.room.server_acl", "") != nil
	if e := a.store.GetState(roomID, "m.room.encryption", ""); e != nil && gjson.GetBytes(e.Content, "algorithm").Str != "" {
		s.Encrypted = true
	}
	if pins := a.store.GetState(roomID, "m.room.pinned_events", ""); pins != nil {
		s.Pinned = len(gjson.GetBytes(pins.Content, "pinned").Array())
	}

	// DM: según m.direct; si no, una invitación marcada is_direct.
	s.DM = a.dmUser(roomID)
	if r.Membership == "invite" {
		if inv := a.store.GetState(roomID, "m.room.member", me); inv != nil {
			s.Inviter = inv.Sender
			s.InviterName = a.memberProfile(roomID, inv.Sender).Name
			if gjson.GetBytes(inv.Content, "is_direct").Bool() && s.DM == "" {
				s.DM = inv.Sender
			}
		}
	}

	// Héroes para nombrar salas sin nombre.
	heroes := r.Heroes
	if len(heroes) == 0 && s.Name == "" {
		for _, m := range a.store.StateOfType(roomID, "m.room.member") {
			if m.StateKey == me {
				continue
			}
			ms := gjson.GetBytes(m.Content, "membership").Str
			if ms == "join" || ms == "invite" {
				heroes = append(heroes, m.StateKey)
			}
			if len(heroes) >= 5 {
				break
			}
		}
		sort.Strings(heroes)
	}
	for _, h := range heroes {
		if h == me {
			continue
		}
		p := a.memberProfile(roomID, h)
		s.Heroes = append(s.Heroes, Hero{ID: h, Name: p.Name, Avatar: p.Avatar})
	}
	if s.DM != "" {
		p := a.memberProfile(roomID, s.DM)
		if s.Name == "" {
			s.Name = p.Name
			if s.Name == "" {
				s.Name = s.DM
			}
		}
		if s.Avatar == "" {
			s.Avatar = p.Avatar
		}
	}
	if s.Name == "" && s.Alias != "" {
		s.Name = s.Alias
	}
	if s.Name == "" && r.SSSName != "" {
		s.Name = r.SSSName
	}
	if s.Avatar == "" && s.Name == "" && len(s.Heroes) == 1 {
		s.Avatar = s.Heroes[0].Avatar
	}
	if s.Joined == 0 {
		n := 0
		for _, m := range a.store.StateOfType(roomID, "m.room.member") {
			if gjson.GetBytes(m.Content, "membership").Str == "join" {
				n++
			}
		}
		s.Joined = n
	}

	// Etiquetas.
	for t := range r.Tags {
		s.Tags = append(s.Tags, t)
	}
	sort.Strings(s.Tags)

	// Espacios.
	for _, p := range a.store.StateOfType(roomID, "m.space.parent") {
		if len(gjson.GetBytes(p.Content, "via").Array()) > 0 {
			s.Parents = append(s.Parents, p.StateKey)
		}
	}
	if s.Type == "m.space" {
		for _, c := range a.store.StateOfType(roomID, "m.space.child") {
			cc := gjson.ParseBytes(c.Content)
			via := cc.Get("via").Array()
			if len(via) == 0 {
				continue
			}
			ch := SpaceChild{ID: c.StateKey, Order: cc.Get("order").Str, Suggested: cc.Get("suggested").Bool()}
			for _, v := range via {
				ch.Via = append(ch.Via, v.Str)
			}
			s.Children = append(s.Children, ch)
		}
		sort.SliceStable(s.Children, func(i, j int) bool {
			oi, oj := s.Children[i].Order, s.Children[j].Order
			if oi != oj {
				if oi == "" {
					return false
				}
				if oj == "" {
					return true
				}
				return oi < oj
			}
			return s.Children[i].ID < s.Children[j].ID
		})
	}

	// Puentes a otras redes (MSC2346).
	for _, typ := range []string{"m.bridge", "uk.half-shot.bridge"} {
		for _, b := range a.store.StateOfType(roomID, typ) {
			bc := gjson.ParseBytes(b.Content)
			proto := bc.Get("protocol.id").Str
			if proto == "" {
				continue
			}
			s.Bridge = &Bridge{Protocol: proto, Name: bc.Get("protocol.displayname").Str,
				Avatar: bc.Get("protocol.avatar_url").Str, Channel: bc.Get("channel.displayname").Str}
			break
		}
		if s.Bridge != nil {
			break
		}
	}

	// Llamadas MatrixRTC activas (miembros de la llamada).
	for _, typ := range []string{"org.matrix.msc3401.call.member", "m.call.member"} {
		for _, m := range a.store.StateOfType(roomID, typ) {
			if string(m.Content) == "{}" || len(m.Content) <= 2 {
				continue
			}
			c := gjson.ParseBytes(m.Content)
			if c.Get("application").Str != "" || len(c.Get("memberships").Array()) > 0 || c.Get("focus_active").Exists() {
				s.Call = append(s.Call, m.Sender)
			}
		}
	}
	s.Widgets = len(a.store.StateOfType(roomID, "im.vector.modular.widgets"))

	// Poder propio.
	s.Power = a.powerLevel(roomID, me)

	// Último mensaje.
	if last := a.store.LastMessage(roomID); last != nil {
		p := &Preview{Sender: last.Sender, Type: last.Type}
		if last.Sender != me {
			p.Name = a.memberProfile(roomID, last.Sender).Name
		}
		if last.DecryptError != "" {
			p.UTD = true
		} else {
			c := gjson.ParseBytes(last.Content)
			p.MsgType = c.Get("msgtype").Str
			body := c.Get("body").Str
			if len(body) > 160 {
				body = string([]rune(body)[:min(len([]rune(body)), 120)])
			}
			p.Body = body
		}
		s.Preview = p
		if last.TS > s.LastTS {
			s.LastTS = last.TS
		}
	}
	if _, lastID := a.store.MaxSortEvent(roomID); lastID != "" {
		if e := a.store.GetEvent(lastID); e != nil && e.TS > s.LastTS && !isStateNoise(e) {
			s.LastTS = e.TS
		}
	}

	// No leídos.
	s.Notify = a.roomNotifyMode(roomID)
	if r.Membership == "join" {
		if r.ReadSort == 0 && r.ReadEvent != "" {
			// El recibo apunta a un evento más viejo que nuestra caché: usamos el conteo del servidor.
			s.Unread = r.SrvNotif
		} else {
			s.Unread = a.store.CountUnread(roomID, me, r.ReadSort)
		}
		s.Highlight = r.LocalHighlight
		if !s.Encrypted && r.SrvHighlight > s.Highlight {
			s.Highlight = r.SrvHighlight
		}
		if s.Unread == 0 {
			s.Highlight = 0
		}
	}
	return s
}

func isStateNoise(e *EventRow) bool {
	return e.StateKey != nil && e.Type == "m.room.member"
}

func (a *Account) powerLevel(roomID, userID string) int {
	pl := a.store.GetState(roomID, "m.room.power_levels", "")
	if pl == nil {
		// Sin power_levels: el creador tiene 100.
		if c := a.store.GetState(roomID, "m.room.create", ""); c != nil && c.Sender == userID {
			return 100
		}
		return 0
	}
	c := gjson.ParseBytes(pl.Content)
	if v := c.Get("users." + gjsonEscape(userID)); v.Exists() {
		return int(v.Int())
	}
	// Salas v12: los creadores tienen poder infinito.
	if cr := a.store.GetState(roomID, "m.room.create", ""); cr != nil {
		cc := gjson.ParseBytes(cr.Content)
		if v := cc.Get("room_version").Str; v == "12" || strings.HasPrefix(v, "org.matrix.hydra") {
			if cr.Sender == userID {
				return 1 << 30
			}
			for _, ac := range cc.Get("additional_creators").Array() {
				if ac.Str == userID {
					return 1 << 30
				}
			}
		}
	}
	return int(c.Get("users_default").Int())
}

func gjsonEscape(s string) string {
	r := strings.NewReplacer(".", `\.`, "*", `\*`, "?", `\?`, "|", `\|`, "#", `\#`, "@", `\@`)
	return r.Replace(s)
}

// roomNotifyMode devuelve "all", "mentions" o "mute" según las reglas push.
func (a *Account) roomNotifyMode(roomID string) string {
	rs := a.pushRules.Load()
	if rs == nil {
		return "default"
	}
	if rs.Override != nil {
		for _, rule := range rs.Override {
			if !rule.Enabled || rule.RuleID != roomID {
				continue
			}
			if !actionsNotify(rule.Actions) {
				return "mute"
			}
		}
	}
	if rule, ok := rs.Room.Map[roomID]; ok && rule != nil && rule.Enabled {
		if actionsNotify(rule.Actions) {
			return "all"
		}
		return "mentions"
	}
	return "default"
}

func actionsNotify(actions pushrules.PushActionArray) bool {
	for _, act := range actions {
		if act.Action == pushrules.ActionNotify {
			return true
		}
	}
	return false
}

func (a *Account) RoomList() []*RoomSummary {
	rows := a.store.AllRooms()
	out := make([]*RoomSummary, 0, len(rows))
	for _, r := range rows {
		if s := a.RoomSummary(r.ID); s != nil {
			out = append(out, s)
		}
	}
	return out
}

// State devuelve el estado completo cacheado de una sala (para ajustes y miembros).
func (a *Account) RoomState(roomID string, types []string) map[string][]StateEntry {
	out := map[string][]StateEntry{}
	for _, t := range types {
		out[t] = a.store.StateOfType(roomID, t)
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// unreadSummary cuenta, como la lista de chats, los chats con algo sin leer (sin los silenciados
// ni los espacios; en los de "solo menciones", solo si te mencionaron) y las menciones.
func (a *Account) unreadSummary() (chats, mentions int) {
	me := a.UserID()
	for _, r := range a.store.AllRooms() {
		switch r.Membership {
		case "invite":
			chats++
			continue
		case "join":
		default:
			continue
		}
		mode := a.roomNotifyMode(r.ID)
		if mode == "mute" {
			continue
		}
		if create := a.store.GetState(r.ID, "m.room.create", ""); create != nil && gjson.GetBytes(create.Content, "type").Str == "m.space" {
			continue
		}
		unread := r.SrvNotif
		if r.ReadSort != 0 || r.ReadEvent == "" {
			unread = a.store.CountUnread(r.ID, me, r.ReadSort)
		}
		hl := r.LocalHighlight
		if r.SrvHighlight > hl {
			hl = r.SrvHighlight
		}
		if unread == 0 {
			hl = 0
		}
		if (unread > 0 && mode != "mentions") || hl > 0 || r.MarkedUnread {
			chats++
		}
		mentions += hl
	}
	return chats, mentions
}

// UnreadSummary suma los chats sin leer y las menciones de todas las cuentas (para la bandeja).
func (m *Manager) UnreadSummary() (chats, mentions int) {
	for _, a := range m.Accounts() {
		c, h := a.unreadSummary()
		chats += c
		mentions += h
	}
	return chats, mentions
}
