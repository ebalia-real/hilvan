package core

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/pushrules"
)

type PresenceInfo struct {
	Presence  string `json:"presence"`
	LastAgo   int64  `json:"last_active_ago,omitempty"`
	Active    bool   `json:"currently_active,omitempty"`
	StatusMsg string `json:"status_msg,omitempty"`
	At        int64  `json:"at"`
}

// syncBatch acumula los cambios de un sync para enviarlos a la interfaz en un solo mensaje.
type syncBatch struct {
	rooms         map[string]bool
	removed       map[string]string // sala -> motivo
	newItems      map[string][]string
	updated       map[string]map[string]bool // sala -> eventos a re-enviar
	typing        map[string][]string
	receipts      map[string]map[string][]string
	presence      map[string]PresenceInfo
	accData       map[string]json.RawMessage
	notify        []*EventRow
	cryptoChanged bool
}

func newSyncBatch() *syncBatch {
	return &syncBatch{
		rooms: map[string]bool{}, removed: map[string]string{}, newItems: map[string][]string{},
		updated: map[string]map[string]bool{}, typing: map[string][]string{}, receipts: map[string]map[string][]string{},
		presence: map[string]PresenceInfo{}, accData: map[string]json.RawMessage{},
	}
}

func (b *syncBatch) touch(roomID, eventID string) {
	if eventID == "" {
		return
	}
	if b.updated[roomID] == nil {
		b.updated[roomID] = map[string]bool{}
	}
	b.updated[roomID][eventID] = true
}

// preparedEvent es un evento ya descifrado y analizado, listo para guardar.
type preparedEvent struct {
	evt     *event.Event // descifrado si se pudo
	row     *EventRow
	isNew   bool
	bundled bool   // vino agrupado en "unsigned" (edición o última respuesta de hilo)
	redacts string // destino si es una redacción
}

func isVerificationEvent(evt *event.Event) bool {
	if strings.HasPrefix(evt.Type.Type, "m.key.verification.") {
		return true
	}
	if evt.Type == event.EventMessage {
		return gjson.GetBytes(rawContent(evt), "msgtype").Str == "m.key.verification.request"
	}
	return false
}

func isCallEvent(t string) bool {
	return strings.HasPrefix(t, "m.call.") || strings.HasPrefix(t, "org.matrix.call.") || t == "org.matrix.msc4075.rtc.notification" || t == "org.matrix.msc4075.call.notify" || t == "m.rtc.notification"
}

// prepareEvent descifra y analiza un evento de sala.
func (a *Account) prepareEvent(ctx context.Context, roomID string, evt *event.Event) *preparedEvent {
	evt.RoomID = id.RoomID(roomID)
	row := &EventRow{
		RoomID:   roomID,
		EventID:  evt.ID.String(),
		Sender:   evt.Sender.String(),
		Type:     evt.Type.Type,
		StateKey: evt.StateKey,
		TS:       evt.Timestamp,
		Content:  rawContent(evt),
		Unsigned: rawUnsigned(evt),
	}
	if evt.Unsigned.RedactedBecause != nil {
		row.RedactedBy = evt.Unsigned.RedactedBecause.ID.String()
	}
	if evt.Type == event.EventEncrypted && row.RedactedBy == "" {
		full, _ := json.Marshal(evt)
		row.Encrypted = full
		if err := evt.Content.ParseRaw(evt.Type); err == nil || strings.Contains(err.Error(), "already parsed") {
			if enc, ok := evt.Content.Parsed.(*event.EncryptedEventContent); ok {
				row.SessionID = enc.SessionID.String()
			}
		}
		dec, err := a.mach.DecryptMegolmEvent(ctx, evt)
		if err != nil {
			row.DecryptError = err.Error()
			a.markUTD(row.SessionID)
		} else {
			applyDecrypted(row, dec)
			evt = dec
		}
	}
	fillRelations(row)
	pe := &preparedEvent{evt: evt, row: row}
	if row.Type == "m.room.redaction" {
		pe.redacts = redactsOf(evt, row.Content)
	}
	return pe
}

func applyDecrypted(row *EventRow, dec *event.Event) {
	row.Type = dec.Type.Type
	row.Content = rawContent(dec)
	row.DecryptError = ""
	row.Trust = trustLabel(dec.Mautrix.TrustState, dec.Mautrix.ForwardedKeys)
	// El m.relates_to cifrado puede venir afuera (en claro) según el cliente emisor.
	if !gjson.GetBytes(row.Content, `m\.relates_to`).Exists() && len(row.Encrypted) > 0 {
		if rel := gjson.GetBytes(row.Encrypted, `content.m\.relates_to`); rel.Exists() {
			var m map[string]any
			if json.Unmarshal(row.Content, &m) == nil {
				var r any
				_ = json.Unmarshal([]byte(rel.Raw), &r)
				m["m.relates_to"] = r
				row.Content, _ = json.Marshal(m)
			}
		}
	}
}

func trustLabel(ts id.TrustState, forwarded bool) string {
	switch {
	case ts == id.TrustStateBlacklisted:
		return "blacklisted"
	case ts == id.TrustStateDeviceKeyMismatch:
		return "mismatch"
	case ts >= id.TrustStateCrossSignedVerified:
		return "verified"
	case ts >= id.TrustStateCrossSignedTOFU:
		return "tofu"
	case ts == id.TrustStateCrossSignedUntrusted:
		return "changed"
	case ts == id.TrustStateBackup:
		return "backup"
	case ts == id.TrustStateForwarded || forwarded:
		return "forwarded"
	case ts == id.TrustStateUnknownDevice:
		return "unknown_device"
	default:
		return "unsigned"
	}
}

func fillRelations(row *EventRow) {
	rel := gjson.GetBytes(row.Content, `m\.relates_to`)
	row.RelType, row.RelTo, row.RelKey, row.ThreadRoot, row.ReplyTo = "", "", "", "", ""
	if row.Type == "m.room.redaction" {
		return
	}
	if !rel.Exists() {
		return
	}
	relType := rel.Get("rel_type").Str
	target := rel.Get("event_id").Str
	inReply := rel.Get(`m\.in_reply_to.event_id`).Str
	switch relType {
	case "m.thread", "io.element.thread":
		row.ThreadRoot = target
		if !rel.Get("is_falling_back").Bool() {
			row.ReplyTo = inReply
		}
	case "m.annotation", "m.replace", "m.reference":
		row.RelType, row.RelTo = relType, target
		row.RelKey = rel.Get("key").Str
	case "":
		row.ReplyTo = inReply
	default:
		row.RelType, row.RelTo = relType, target
	}
}

func redactsOf(evt *event.Event, content json.RawMessage) string {
	if evt.Redacts != "" {
		return evt.Redacts.String()
	}
	return gjson.GetBytes(content, "redacts").Str
}

// processSync es el manejador principal de /sync (y de Sliding Sync adaptado).
func (a *Account) processSync(ctx context.Context, resp *mautrix.RespSync, since string) bool {
	start := time.Now()
	batch := newSyncBatch()
	me := a.UserID()
	live := since != "" || a.initialDone.Load()

	// Fase 1: descifrar y analizar fuera de la transacción (puede tocar la red).
	type roomWork struct {
		roomID   string
		jr       *mautrix.SyncJoinedRoom
		state    []*event.Event
		timeline []*preparedEvent
	}
	var work []*roomWork
	for roomID, jr := range resp.Rooms.Join {
		rw := &roomWork{roomID: roomID.String(), jr: jr}
		if jr.StateAfter != nil {
			rw.state = jr.StateAfter.Events
		} else {
			rw.state = jr.State.Events
		}
		for _, evt := range jr.Timeline.Events {
			pe := a.prepareEvent(ctx, rw.roomID, evt)
			// Las ediciones e hilos que el servidor agrupa en "unsigned" también los guardamos.
			rw.timeline = append(rw.timeline, a.bundledRelations(ctx, rw.roomID, evt)...)
			rw.timeline = append(rw.timeline, pe)
		}
		work = append(work, rw)
	}

	// Fase 2: escritura en una sola transacción corta.
	_ = a.store.WithTx(ctx, func() error {
		for _, evt := range resp.AccountData.Events {
			a.handleAccountData(evt, batch)
		}
		for _, rw := range work {
			a.applyJoinedRoom(ctx, rw.roomID, rw.jr, rw.state, rw.timeline, batch, live)
		}
		for roomID, ir := range resp.Rooms.Invite {
			a.applyInvite(roomID.String(), ir.State.Events, batch)
		}
		for roomID, kr := range resp.Rooms.Knock {
			a.store.EnsureRoom(roomID.String(), "knock")
			for _, evt := range kr.State.Events {
				a.putStateEvent(roomID.String(), evt)
			}
			batch.rooms[roomID.String()] = true
		}
		for roomID, lr := range resp.Rooms.Leave {
			reason := "leave"
			for _, evt := range append(lr.State.Events, lr.Timeline.Events...) {
				if evt.Type == event.StateMember && evt.GetStateKey() == me {
					m := gjson.GetBytes(rawContent(evt), "membership").Str
					if m == "ban" {
						reason = "ban"
					} else if m == "leave" && evt.Sender.String() != me {
						reason = "kick"
					}
				}
			}
			if a.store.GetRoom(roomID.String()) != nil {
				a.store.ForgetRoom(roomID.String())
				batch.removed[roomID.String()] = reason
			}
		}
		return nil
	})

	for _, evt := range resp.Presence.Events {
		var p PresenceInfo
		_ = json.Unmarshal(rawContent(evt), &p)
		p.At = time.Now().UnixMilli()
		batch.presence[evt.Sender.String()] = p
		a.presenceMu.Lock()
		a.presence[evt.Sender.String()] = p
		a.presenceMu.Unlock()
	}

	// Eventos de verificación y llamadas que llegaron cifrados: despacharlos ya descifrados.
	for _, rw := range work {
		for _, pe := range rw.timeline {
			if !pe.isNew || pe.row.DecryptError != "" {
				continue
			}
			if pe.evt.Mautrix.WasEncrypted || pe.row.Encrypted != nil {
				if isVerificationEvent(pe.evt) && pe.evt.Sender.String() != me {
					_ = pe.evt.Content.ParseRaw(pe.evt.Type)
					a.syncer.Dispatch(ctx, pe.evt)
				}
			}
			if live && isCallEvent(pe.row.Type) {
				a.m.Calls.HandleRoomEvent(a, pe.row)
			}
		}
	}

	a.flushBatch(batch)
	if len(resp.DeviceLists.Changed) > 0 {
		changed := append([]id.UserID(nil), resp.DeviceLists.Changed...)
		go a.onDeviceListsChanged(changed)
	}
	if !a.initialDone.Load() {
		a.initialDone.Store(true)
		a.m.Broadcast(Push{Ev: "account.ready", A: me})
		go func() {
			a.m.Broadcast(Push{Ev: "crypto.status", A: me, D: a.CryptoStatus(a.ctx)})
		}()
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		a.log.Debug().Dur("took", d).Int("rooms", len(work)).Msg("Sync procesado")
	}
	return true
}

// bundledRelations convierte la última edición agrupada por el servidor en un evento local.
func (a *Account) bundledRelations(ctx context.Context, roomID string, evt *event.Event) []*preparedEvent {
	var out []*preparedEvent
	raw := rawUnsigned(evt)
	rel := gjson.GetBytes(raw, `m\.relations`)
	if !rel.Exists() {
		return nil
	}
	if rep := rel.Get(`m\.replace`); rep.Exists() && rep.Get("event_id").Exists() && rep.Get("type").Exists() {
		var e event.Event
		if json.Unmarshal([]byte(rep.Raw), &e) == nil && e.ID != "" {
			pe := a.prepareEvent(ctx, roomID, &e)
			pe.bundled = true
			out = append(out, pe)
		}
	}
	if th := rel.Get(`m\.thread.latest_event`); th.Exists() {
		var e event.Event
		if json.Unmarshal([]byte(th.Raw), &e) == nil && e.ID != "" {
			pe := a.prepareEvent(ctx, roomID, &e)
			pe.bundled = true
			out = append(out, pe)
		}
	}
	return out
}

func (a *Account) putStateEvent(roomID string, evt *event.Event) {
	if evt.StateKey == nil {
		return
	}
	a.store.PutState(roomID, StateEntry{
		Type: evt.Type.Type, StateKey: *evt.StateKey, EventID: evt.ID.String(),
		Sender: evt.Sender.String(), TS: evt.Timestamp, Content: rawContent(evt),
	})
}

func (a *Account) applyInvite(roomID string, state []*event.Event, batch *syncBatch) {
	prev := a.store.GetRoom(roomID)
	if prev != nil && prev.Membership == "join" {
		return
	}
	a.store.EnsureRoom(roomID, "invite")
	for _, evt := range state {
		a.putStateEvent(roomID, evt)
		if evt.Type == event.StateMember && evt.GetStateKey() == a.UserID() {
			a.store.UpdateRoom(roomID, map[string]any{"bump_ts": evt.Timestamp})
		}
	}
	batch.rooms[roomID] = true
	if prev == nil && a.initialDone.Load() {
		batch.notify = append(batch.notify, &EventRow{RoomID: roomID, Type: "invite", TS: time.Now().UnixMilli()})
	}
}

func (a *Account) applyJoinedRoom(ctx context.Context, roomID string, jr *mautrix.SyncJoinedRoom, state []*event.Event,
	timeline []*preparedEvent, batch *syncBatch, live bool) {
	me := a.UserID()
	prev := a.store.GetRoom(roomID)
	a.store.EnsureRoom(roomID, "join")
	batch.rooms[roomID] = true
	fields := map[string]any{}
	if jr.Summary.Heroes != nil {
		heroes := make([]string, 0, len(jr.Summary.Heroes))
		for _, h := range jr.Summary.Heroes {
			heroes = append(heroes, h.String())
		}
		fields["heroes"] = heroes
	}
	if jr.Summary.JoinedMemberCount != nil {
		fields["joined_count"] = *jr.Summary.JoinedMemberCount
	}
	if jr.Summary.InvitedMemberCount != nil {
		fields["invited_count"] = *jr.Summary.InvitedMemberCount
	}
	if jr.UnreadNotifications != nil {
		fields["srv_notif"] = jr.UnreadNotifications.NotificationCount
		fields["srv_highlight"] = jr.UnreadNotifications.HighlightCount
	}
	for _, evt := range state {
		a.putStateEvent(roomID, evt)
	}
	hasEvents := prev != nil && prev.Membership == "join" && a.store.MinSort(roomID) != 0
	if jr.Timeline.Limited || !hasEvents {
		if jr.Timeline.Limited && hasEvents {
			a.store.ClearTimeline(roomID)
			batch.removed[roomID] = "reset"
		}
		fields["prev_batch"] = jr.Timeline.PrevBatch
		fields["history_complete"] = 0
	}
	a.store.UpdateRoom(roomID, fields)
	if prev != nil && prev.Membership != "join" {
		batch.removed[roomID] = "reset"
	}

	readSort := int64(0)
	if prev != nil {
		readSort = prev.ReadSort
	}
	var lastOwnSort int64
	localHL := 0
	for _, pe := range timeline {
		row := pe.row
		evt := pe.evt
		if jr.StateAfter == nil && row.StateKey != nil {
			a.putStateEvent(roomID, evt)
		}
		pe.isNew = a.ingestRow(row, pe.redacts, batch)
		if !pe.isNew || pe.bundled {
			continue
		}
		if row.Sender == me {
			lastOwnSort = row.Sort
		} else if live && a.initialDone.Load() && row.Sort > readSort && isNotifiable(row) {
			if hl := a.evalPush(roomID, row); hl.notify {
				batch.notify = append(batch.notify, row)
				if hl.highlight {
					localHL++
				}
			}
		}
	}
	for _, evt := range jr.Ephemeral.Events {
		a.handleEphemeral(roomID, evt, batch)
	}
	for _, evt := range jr.AccountData.Events {
		a.handleRoomAccountData(roomID, evt, batch)
	}
	if lastOwnSort > 0 {
		r := a.store.GetRoom(roomID)
		if r != nil && lastOwnSort > r.ReadSort {
			a.store.UpdateRoom(roomID, map[string]any{"read_sort": lastOwnSort, "local_highlight": 0, "marked_unread": 0})
		}
	} else if localHL > 0 {
		r := a.store.GetRoom(roomID)
		if r != nil {
			a.store.UpdateRoom(roomID, map[string]any{"local_highlight": r.LocalHighlight + localHL})
		}
	}
}

// ingestRow guarda un evento preparado y aplica sus efectos (redacciones, ecos locales, búsqueda).
func (a *Account) ingestRow(row *EventRow, redacts string, batch *syncBatch) bool {
	roomID := row.RoomID
	if existing := a.store.GetEvent(row.EventID); existing != nil {
		// Ya lo teníamos: actualizar solo si ahora pudimos descifrarlo.
		if existing.DecryptError != "" && row.DecryptError == "" {
			row.Sort = existing.Sort
			a.store.PutEvent(row)
			a.indexRow(row)
			batch.touch(roomID, row.EventID)
		}
		return false
	}
	// Eco de un mensaje propio enviado desde aquí.
	txn := gjson.GetBytes(row.Unsigned, "transaction_id").Str
	if txn != "" && row.Sender == a.UserID() {
		if local := a.store.GetEventByTxn(txn); local != nil {
			a.store.DeleteEvent(local.EventID)
			row.TxnID = txn
		}
	}
	if row.Type == "m.room.redaction" && redacts != "" {
		a.applyRedaction(roomID, redacts, row.EventID, batch)
	}
	a.store.PutEvent(row)
	if row.Type == "m.room.redaction" {
		batch.touch(roomID, row.EventID)
		return true
	}
	a.indexRow(row)
	switch {
	case row.RelType == "m.replace" || row.RelType == "m.annotation" || row.RelType == "m.reference":
		batch.touch(roomID, row.RelTo)
		if t := a.store.GetEvent(row.RelTo); t != nil && t.ThreadRoot != "" {
			batch.touch(roomID, t.ThreadRoot)
		}
	case row.ThreadRoot != "":
		batch.touch(roomID, row.ThreadRoot)
		batch.newItems[roomID] = append(batch.newItems[roomID], row.EventID)
	default:
		batch.newItems[roomID] = append(batch.newItems[roomID], row.EventID)
	}
	return true
}

func (a *Account) indexRow(row *EventRow) {
	if row.RedactedBy != "" || row.DecryptError != "" {
		return
	}
	switch row.Type {
	case "m.room.message":
		target := row.EventID
		content := gjson.ParseBytes(row.Content)
		if row.RelType == "m.replace" {
			target = row.RelTo
			content = content.Get(`m\.new_content`)
		}
		body := content.Get("body").Str
		if mt := content.Get("msgtype").Str; mt == "m.file" || mt == "m.image" || mt == "m.video" || mt == "m.audio" {
			if fn := content.Get("filename").Str; fn != "" && fn != body {
				body = fn + " " + body
			}
		}
		a.store.IndexText(row.RoomID, target, row.Sender, row.TS, body)
	case "org.matrix.msc3381.poll.start", "m.poll.start":
		c := gjson.ParseBytes(row.Content)
		q := c.Get(`org\.matrix\.msc3381\.poll\.start.question.org\.matrix\.msc1767\.text`).Str
		if q == "" {
			q = c.Get(`m\.poll.question.m\.text.0.body`).Str
		}
		a.store.IndexText(row.RoomID, row.EventID, row.Sender, row.TS, q)
	}
}

func (a *Account) applyRedaction(roomID, target, redactedBy string, batch *syncBatch) {
	t := a.store.GetEvent(target)
	if t == nil {
		return
	}
	keep := redactedContent(t.Type, t.Content)
	a.store.Redact(target, redactedBy, keep)
	batch.touch(roomID, target)
	if t.RelTo != "" {
		batch.touch(roomID, t.RelTo)
		if t.RelType == "m.replace" {
			// Si se borró una edición, re-indexar el texto original.
			if orig := a.store.GetEvent(t.RelTo); orig != nil {
				a.indexRow(orig)
			}
		}
	}
	if t.ThreadRoot != "" {
		batch.touch(roomID, t.ThreadRoot)
	}
}

// redactedContent conserva solo las claves que la especificación preserva.
func redactedContent(typ string, content json.RawMessage) json.RawMessage {
	keepKeys := map[string][]string{
		"m.room.member":             {"membership", "join_authorised_via_users_server"},
		"m.room.create":             {"creator", "room_version", "type", "predecessor"},
		"m.room.join_rules":         {"join_rule", "allow"},
		"m.room.power_levels":       {"ban", "events", "events_default", "invite", "kick", "redact", "state_default", "users", "users_default"},
		"m.room.history_visibility": {"history_visibility"},
	}
	keys, ok := keepKeys[typ]
	if !ok {
		return json.RawMessage("{}")
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(content, &m) != nil {
		return json.RawMessage("{}")
	}
	out := map[string]json.RawMessage{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	b, _ := json.Marshal(out)
	return b
}

func isNotifiable(row *EventRow) bool {
	if row.RelType == "m.replace" || row.RelType == "m.annotation" {
		return false
	}
	switch row.Type {
	case "m.room.message", "m.sticker", "m.room.encrypted", "org.matrix.msc3381.poll.start", "m.poll.start":
		return true
	}
	return false
}

func (a *Account) handleEphemeral(roomID string, evt *event.Event, batch *syncBatch) {
	content := rawContent(evt)
	switch evt.Type.Type {
	case "m.typing":
		var c struct {
			UserIDs []string `json:"user_ids"`
		}
		_ = json.Unmarshal(content, &c)
		users := make([]string, 0, len(c.UserIDs))
		for _, u := range c.UserIDs {
			if u != a.UserID() {
				users = append(users, u)
			}
		}
		a.typingMu.Lock()
		a.typing[roomID] = users
		a.typingMu.Unlock()
		batch.typing[roomID] = users
	case "m.receipt":
		var c map[string]map[string]map[string]struct {
			TS       int64  `json:"ts"`
			ThreadID string `json:"thread_id"`
		}
		_ = json.Unmarshal(content, &c)
		for eventID, types := range c {
			for rtype, users := range types {
				if rtype != "m.read" && rtype != "m.read.private" {
					continue
				}
				for user, info := range users {
					if info.ThreadID != "" && info.ThreadID != "main" {
						continue
					}
					a.store.PutReceipt(roomID, user, eventID, info.TS)
					if batch.receipts[roomID] == nil {
						batch.receipts[roomID] = map[string][]string{}
					}
					batch.receipts[roomID][user] = []string{eventID}
					if user == a.UserID() {
						if e := a.store.GetEvent(eventID); e != nil {
							if r := a.store.GetRoom(roomID); r != nil && e.Sort >= r.ReadSort {
								a.store.UpdateRoom(roomID, map[string]any{"read_sort": e.Sort, "read_event": eventID, "local_highlight": 0})
							}
						} else {
							a.store.UpdateRoom(roomID, map[string]any{"read_event": eventID})
						}
					}
				}
			}
		}
	}
}

func (a *Account) handleRoomAccountData(roomID string, evt *event.Event, batch *syncBatch) {
	content := rawContent(evt)
	a.store.PutRoomAccountData(roomID, evt.Type.Type, content)
	switch evt.Type.Type {
	case "m.tag":
		var c struct {
			Tags map[string]json.RawMessage `json:"tags"`
		}
		_ = json.Unmarshal(content, &c)
		if c.Tags == nil {
			c.Tags = map[string]json.RawMessage{}
		}
		a.store.UpdateRoom(roomID, map[string]any{"tags": c.Tags})
	case "m.marked_unread", "com.famedly.marked_unread":
		a.store.UpdateRoom(roomID, map[string]any{"marked_unread": gjson.GetBytes(content, "unread").Bool()})
	case "m.fully_read":
		if eid := gjson.GetBytes(content, "event_id").Str; eid != "" {
			if e := a.store.GetEvent(eid); e != nil {
				if r := a.store.GetRoom(roomID); r != nil && e.Sort > r.ReadSort {
					a.store.UpdateRoom(roomID, map[string]any{"read_sort": e.Sort, "read_event": eid})
				}
			}
		}
	}
	batch.rooms[roomID] = true
}

func (a *Account) handleAccountData(evt *event.Event, batch *syncBatch) {
	content := rawContent(evt)
	a.store.PutAccountData(evt.Type.Type, content)
	batch.accData[evt.Type.Type] = content
	switch evt.Type.Type {
	case "m.direct":
		a.loadDirect(content)
		for _, r := range a.store.AllRooms() {
			batch.rooms[r.ID] = true
		}
	case "m.push_rules":
		a.loadPushRules(content)
		for _, r := range a.store.AllRooms() {
			batch.rooms[r.ID] = true
		}
	case "m.ignored_user_list":
		a.loadIgnored(content)
	case "m.secret_storage.default_key", "m.cross_signing.master", "m.cross_signing.self_signing",
		"m.cross_signing.user_signing", "m.megolm_backup.v1":
		batch.cryptoChanged = true
	}
}

func (a *Account) loadCachedAccountData() {
	if c := a.store.GetAccountData("m.direct"); c != nil {
		a.loadDirect(c)
	}
	if c := a.store.GetAccountData("m.push_rules"); c != nil {
		a.loadPushRules(c)
	}
	if c := a.store.GetAccountData("m.ignored_user_list"); c != nil {
		a.loadIgnored(c)
	}
}

func (a *Account) loadDirect(content json.RawMessage) {
	var direct map[string][]string
	_ = json.Unmarshal(content, &direct)
	rooms := map[string]string{}
	for user, rs := range direct {
		for _, r := range rs {
			rooms[r] = user
		}
	}
	a.dmMu.Lock()
	a.dmRooms = rooms
	a.dmMu.Unlock()
}

func (a *Account) dmUser(roomID string) string {
	a.dmMu.RLock()
	defer a.dmMu.RUnlock()
	return a.dmRooms[roomID]
}

func (a *Account) loadPushRules(content json.RawMessage) {
	var wrapper struct {
		Global *pushrules.PushRuleset `json:"global"`
	}
	if err := json.Unmarshal(content, &wrapper); err == nil && wrapper.Global != nil {
		a.pushRules.Store(wrapper.Global)
	}
}

func (a *Account) loadIgnored(content json.RawMessage) {
	var c struct {
		Ignored map[string]json.RawMessage `json:"ignored_users"`
	}
	_ = json.Unmarshal(content, &c)
	m := map[string]bool{}
	for u := range c.Ignored {
		m[u] = true
	}
	a.ignoredMu.Lock()
	a.ignored = m
	a.ignoredMu.Unlock()
}

func (a *Account) isIgnored(user string) bool {
	a.ignoredMu.RLock()
	defer a.ignoredMu.RUnlock()
	return a.ignored[user]
}

// flushBatch envía a la interfaz un único mensaje con todos los cambios del sync.
func (a *Account) flushBatch(batch *syncBatch) {
	out := map[string]any{}
	if len(batch.rooms) > 0 {
		var sums []*RoomSummary
		for roomID := range batch.rooms {
			if _, gone := batch.removed[roomID]; gone && batch.removed[roomID] != "reset" {
				continue
			}
			if s := a.RoomSummary(roomID); s != nil {
				sums = append(sums, s)
			}
		}
		out["rooms"] = sums
	}
	removed := map[string]string{}
	resets := []string{}
	for r, why := range batch.removed {
		if why == "reset" {
			resets = append(resets, r)
		} else {
			removed[r] = why
		}
	}
	if len(removed) > 0 {
		out["removed"] = removed
	}
	if len(resets) > 0 {
		out["reset"] = resets
	}
	timeline := map[string]any{}
	profiles := map[string]map[string]Profile{}
	for roomID, ids := range batch.newItems {
		rows := make([]*EventRow, 0, len(ids))
		for _, eid := range ids {
			if e := a.store.GetEvent(eid); e != nil {
				rows = append(rows, e)
				delete(batch.updated[roomID], eid)
			}
		}
		items, profs := a.buildItems(roomID, rows)
		timeline[roomID] = map[string]any{"new": items}
		profiles[roomID] = profs
	}
	for roomID, ids := range batch.updated {
		if len(ids) == 0 {
			continue
		}
		rows := make([]*EventRow, 0, len(ids))
		for eid := range ids {
			if e := a.store.GetEvent(eid); e != nil {
				rows = append(rows, e)
			}
		}
		items, profs := a.buildItems(roomID, rows)
		t, _ := timeline[roomID].(map[string]any)
		if t == nil {
			t = map[string]any{}
			timeline[roomID] = t
		}
		t["updated"] = items
		if profiles[roomID] == nil {
			profiles[roomID] = profs
		} else {
			for k, v := range profs {
				profiles[roomID][k] = v
			}
		}
	}
	if len(timeline) > 0 {
		out["timeline"] = timeline
		out["profiles"] = profiles
	}
	if len(batch.typing) > 0 {
		out["typing"] = batch.typing
	}
	if len(batch.receipts) > 0 {
		out["receipts"] = batch.receipts
	}
	if len(batch.presence) > 0 {
		out["presence"] = batch.presence
	}
	if len(batch.accData) > 0 {
		out["account_data"] = batch.accData
	}
	if len(out) > 0 {
		a.m.Broadcast(Push{Ev: "sync", A: a.UserID(), D: out})
	}
	if batch.cryptoChanged {
		go a.m.Broadcast(Push{Ev: "crypto.status", A: a.UserID(), D: a.CryptoStatus(a.ctx)})
	}
	if len(batch.notify) > 0 {
		go a.notifyRows(batch.notify)
	}
}
