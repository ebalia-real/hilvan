package core

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/pushrules"
)

type CreateRoomParams struct {
	Name      string   `json:"name"`
	Topic     string   `json:"topic"`
	Alias     string   `json:"alias"`
	Public    bool     `json:"public"`
	Encrypted bool     `json:"encrypted"`
	Space     bool     `json:"space"`
	Parent    string   `json:"parent"`
	Invite    []string `json:"invite"`
	Direct    bool     `json:"direct"`
	Avatar    string   `json:"avatar"`
	Knock     bool     `json:"knock"`
	Federate  *bool    `json:"federate,omitempty"`
	// Retención: los mensajes se borran pasados estos días (0 = para siempre).
	RetentionDays int `json:"retention_days,omitempty"`
}

func (a *Account) CreateRoom(ctx context.Context, p CreateRoomParams) (string, error) {
	if pol := a.m.Policy(); pol.ForceEncryption && !p.Public {
		p.Encrypted = true // la organización exige cifrado de extremo a extremo
	} else if pol.DisableEncryption {
		p.Encrypted = false
	}
	if a.m.Policy().Disabled("public_rooms") && p.Public {
		return "", errors.New("tu organización no permite crear salas públicas")
	}
	if p.RetentionDays == 0 {
		p.RetentionDays = a.m.Policy().RetentionDays
	}
	req := &mautrix.ReqCreateRoom{
		Name:     strings.TrimSpace(p.Name),
		Topic:    strings.TrimSpace(p.Topic),
		Invite:   toUserIDs(p.Invite),
		IsDirect: p.Direct,
	}
	if p.Public {
		req.Preset = "public_chat"
		req.Visibility = "public"
		if p.Alias != "" {
			req.RoomAliasName = strings.TrimPrefix(strings.Split(p.Alias, ":")[0], "#")
		}
	} else if p.Direct {
		req.Preset = "trusted_private_chat"
	} else {
		req.Preset = "private_chat"
	}
	if p.Space {
		req.CreationContent = map[string]any{"type": "m.space"}
		req.PowerLevelOverride = &event.PowerLevelsEventContent{EventsDefault: 100}
	}
	if p.Federate != nil && !*p.Federate {
		if req.CreationContent == nil {
			req.CreationContent = map[string]any{}
		}
		req.CreationContent["m.federate"] = false
	}
	if p.Encrypted && !p.Space {
		req.InitialState = append(req.InitialState, &event.Event{
			Type: event.StateEncryption, StateKey: ptr(""),
			Content: event.Content{Parsed: &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}},
		})
	}
	if p.RetentionDays > 0 && !p.Space {
		req.InitialState = append(req.InitialState, &event.Event{
			Type: event.Type{Type: "m.room.retention", Class: event.StateEventType}, StateKey: ptr(""),
			Content: event.Content{Raw: map[string]any{"max_lifetime": int64(p.RetentionDays) * 24 * 3600 * 1000}},
		})
	}
	if p.Avatar != "" {
		req.InitialState = append(req.InitialState, &event.Event{
			Type: event.StateRoomAvatar, StateKey: ptr(""),
			Content: event.Content{Raw: map[string]any{"url": p.Avatar}},
		})
	}
	if p.Knock && !p.Public {
		req.InitialState = append(req.InitialState, &event.Event{
			Type: event.StateJoinRules, StateKey: ptr(""),
			Content: event.Content{Raw: map[string]any{"join_rule": "knock"}},
		})
	}
	if p.Parent != "" {
		via := []string{a.cli.UserID.Homeserver()}
		req.InitialState = append(req.InitialState, &event.Event{
			Type: event.StateSpaceParent, StateKey: ptr(p.Parent),
			Content: event.Content{Raw: map[string]any{"via": via, "canonical": true}},
		})
		if !p.Public && !p.Space {
			req.InitialState = append(req.InitialState, &event.Event{
				Type: event.StateJoinRules, StateKey: ptr(""),
				Content: event.Content{Raw: map[string]any{"join_rule": "restricted", "allow": []map[string]any{
					{"type": "m.room_membership", "room_id": p.Parent},
				}}},
			})
		}
	}
	resp, err := a.cli.CreateRoom(ctx, req)
	if err != nil {
		return "", friendlyError(err)
	}
	roomID := resp.RoomID.String()
	if p.Parent != "" {
		_, _ = a.cli.SendStateEvent(ctx, id.RoomID(p.Parent), event.StateSpaceChild, roomID,
			map[string]any{"via": []string{a.cli.UserID.Homeserver()}})
	}
	if p.Direct && len(p.Invite) == 1 {
		_ = a.addDirect(ctx, p.Invite[0], roomID)
	}
	return roomID, nil
}

func ptr[T any](v T) *T { return &v }

func toUserIDs(in []string) []id.UserID {
	out := make([]id.UserID, 0, len(in))
	for _, u := range in {
		u = strings.TrimSpace(u)
		if u != "" {
			out = append(out, id.UserID(u))
		}
	}
	return out
}

func (a *Account) addDirect(ctx context.Context, userID, roomID string) error {
	var direct map[string][]string
	if c := a.store.GetAccountData("m.direct"); c != nil {
		_ = json.Unmarshal(c, &direct)
	}
	if direct == nil {
		direct = map[string][]string{}
	}
	for _, r := range direct[userID] {
		if r == roomID {
			return nil
		}
	}
	direct[userID] = append(direct[userID], roomID)
	if err := a.cli.SetAccountData(ctx, "m.direct", direct); err != nil {
		return err
	}
	b, _ := json.Marshal(direct)
	a.store.PutAccountData("m.direct", b)
	a.loadDirect(b)
	return nil
}

// OpenDM busca un chat directo existente con el usuario o crea uno cifrado.
func (a *Account) OpenDM(ctx context.Context, userID string) (string, error) {
	a.dmMu.RLock()
	var candidates []string
	for room, u := range a.dmRooms {
		if u == userID {
			candidates = append(candidates, room)
		}
	}
	a.dmMu.RUnlock()
	for _, r := range candidates {
		if row := a.store.GetRoom(r); row != nil && row.Membership == "join" {
			return r, nil
		}
	}
	if _, err := a.cli.GetProfile(ctx, id.UserID(userID)); err != nil {
		var httpErr mautrix.HTTPError
		if errors.As(err, &httpErr) && httpErr.IsStatus(404) {
			return "", errors.New("ese usuario no existe")
		}
	}
	return a.CreateRoom(ctx, CreateRoomParams{Direct: true, Encrypted: true, Invite: []string{userID}})
}

func (a *Account) Join(ctx context.Context, target string, via []string, reason string) (string, error) {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "https://matrix.to/#/") {
		target = strings.TrimPrefix(target, "https://matrix.to/#/")
		if i := strings.Index(target, "?"); i > 0 {
			for _, kv := range strings.Split(target[i+1:], "&") {
				if strings.HasPrefix(kv, "via=") {
					via = append(via, strings.TrimPrefix(kv, "via="))
				}
			}
			target = target[:i]
		}
		if i := strings.Index(target, "/$"); i > 0 {
			target = target[:i]
		}
	}
	if target == "" {
		return "", errors.New("indicá una sala")
	}
	// ¿Es una invitación a chat directo? Entonces lo registramos en m.direct al aceptar.
	var dmWith string
	if inv := a.store.GetState(target, "m.room.member", a.UserID()); inv != nil &&
		gjson.GetBytes(inv.Content, "membership").Str == "invite" && gjson.GetBytes(inv.Content, "is_direct").Bool() {
		dmWith = inv.Sender
	}
	resp, err := a.cli.JoinRoom(ctx, target, &mautrix.ReqJoinRoom{Via: via, Reason: reason})
	if err != nil {
		return "", friendlyError(err)
	}
	if dmWith != "" {
		if err := a.addDirect(ctx, dmWith, resp.RoomID.String()); err != nil {
			a.log.Warn().Err(err).Msg("No se pudo marcar el chat como directo")
		}
	}
	return resp.RoomID.String(), nil
}

func (a *Account) Knock(ctx context.Context, target string, via []string, reason string) error {
	_, err := a.cli.KnockRoom(ctx, target, &mautrix.ReqKnockRoom{Via: via, Reason: reason})
	return friendlyErrorNil(err)
}

func (a *Account) Leave(ctx context.Context, roomID string, forget bool) error {
	_, err := a.cli.LeaveRoom(ctx, id.RoomID(roomID))
	if err != nil {
		var httpErr mautrix.HTTPError
		if !(errors.As(err, &httpErr) && httpErr.IsStatus(404)) {
			return friendlyError(err)
		}
	}
	if forget {
		_, _ = a.cli.ForgetRoom(ctx, id.RoomID(roomID))
	}
	a.store.ForgetRoom(roomID)
	a.m.Broadcast(Push{Ev: "sync", A: a.UserID(), D: map[string]any{"removed": map[string]string{roomID: "leave"}}})
	return nil
}

func (a *Account) Invite(ctx context.Context, roomID, userID, reason string) error {
	_, err := a.cli.InviteUser(ctx, id.RoomID(roomID), &mautrix.ReqInviteUser{UserID: id.UserID(userID), Reason: reason})
	if err == nil && a.m.Settings().ShareHistoryOnInvite {
		go a.shareHistoryWith(roomID, userID)
	}
	return friendlyErrorNil(err)
}

// shareHistoryWith (MSC4268): envía al invitado, cifradas, las claves de la historia
// compartible de la sala, para que pueda leer los mensajes anteriores a su llegada.
func (a *Account) shareHistoryWith(roomID, userID string) {
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	sessions, err := a.mach.CryptoStore.GetGroupSessionsForRoom(ctx, id.RoomID(roomID)).AsList()
	if err != nil {
		return
	}
	var keys []*crypto.ExportedSession
	for _, s := range sessions {
		if s.SharedHistory == nil || !*s.SharedHistory {
			continue
		}
		exp, err := s.Internal.Export(s.Internal.FirstKnownIndex())
		if err != nil {
			continue
		}
		keys = append(keys, &crypto.ExportedSession{
			Algorithm: id.AlgorithmMegolmV1, RoomID: s.RoomID, SenderKey: s.SenderKey,
			SenderClaimedKeys: crypto.SenderClaimedKeys{Ed25519: s.SigningKey}, SessionID: s.ID(), SessionKey: string(exp),
		})
	}
	if len(keys) == 0 {
		return
	}
	data, _ := json.Marshal(&crypto.RoomKeyBundle{RoomKeys: keys})
	ef := attachment.NewEncryptedFile()
	ef.EncryptInPlace(data)
	up, err := a.cli.UploadMedia(ctx, mautrix.ReqUploadMedia{ContentBytes: data, ContentType: "application/octet-stream"})
	if err != nil {
		a.log.Warn().Err(err).Msg("No se pudo subir el paquete de claves")
		return
	}
	content := &event.RoomKeyBundleEventContent{
		File:   event.EncryptedFileInfo{EncryptedFile: *ef, URL: up.ContentURI.CUString()},
		RoomID: id.RoomID(roomID),
	}
	devs, err := a.mach.FetchKeys(ctx, []id.UserID{id.UserID(userID)}, true)
	if err != nil {
		return
	}
	sent := 0
	for _, dev := range devs[id.UserID(userID)] {
		// Solo a dispositivos verificados por su dueño (MSC4153/MSC4268).
		if t, _ := a.mach.ResolveTrustContext(ctx, dev); t < id.TrustStateCrossSignedTOFU {
			continue
		}
		if err := a.mach.SendEncryptedToDevice(ctx, dev, event.ToDeviceRoomKeyBundle, event.Content{Parsed: content}); err == nil {
			sent++
		}
	}
	a.log.Info().Int("keys", len(keys)).Int("devices", sent).Str("room", roomID).Msg("Historial compartido con el invitado")
}

func (a *Account) Kick(ctx context.Context, roomID, userID, reason string) error {
	_, err := a.cli.KickUser(ctx, id.RoomID(roomID), &mautrix.ReqKickUser{UserID: id.UserID(userID), Reason: reason})
	return friendlyErrorNil(err)
}

func (a *Account) Ban(ctx context.Context, roomID, userID, reason string, ban bool) error {
	var err error
	if ban {
		_, err = a.cli.BanUser(ctx, id.RoomID(roomID), &mautrix.ReqBanUser{UserID: id.UserID(userID), Reason: reason})
	} else {
		_, err = a.cli.UnbanUser(ctx, id.RoomID(roomID), &mautrix.ReqUnbanUser{UserID: id.UserID(userID), Reason: reason})
	}
	return friendlyErrorNil(err)
}

func (a *Account) SetState(ctx context.Context, roomID, typ, stateKey string, content json.RawMessage) error {
	var c map[string]any
	if err := json.Unmarshal(content, &c); err != nil {
		return err
	}
	_, err := a.cli.SendStateEvent(ctx, id.RoomID(roomID), event.Type{Type: typ, Class: event.StateEventType}, stateKey, c)
	if err == nil {
		a.store.PutState(roomID, StateEntry{Type: typ, StateKey: stateKey, Sender: a.UserID(), Content: content})
		batch := newSyncBatch()
		batch.rooms[roomID] = true
		a.flushBatch(batch)
	}
	return friendlyErrorNil(err)
}

func (a *Account) SetPowerLevel(ctx context.Context, roomID, userID string, level int) error {
	var pl map[string]any
	if e := a.store.GetState(roomID, "m.room.power_levels", ""); e != nil {
		_ = json.Unmarshal(e.Content, &pl)
	} else if err := a.cli.StateEvent(ctx, id.RoomID(roomID), event.StatePowerLevels, "", &pl); err != nil {
		return err
	}
	users, _ := pl["users"].(map[string]any)
	if users == nil {
		users = map[string]any{}
	}
	def, _ := pl["users_default"].(float64)
	if level == int(def) {
		delete(users, userID)
	} else {
		users[userID] = level
	}
	pl["users"] = users
	b, _ := json.Marshal(pl)
	return a.SetState(ctx, roomID, "m.room.power_levels", "", b)
}

type MemberInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Avatar     string `json:"avatar,omitempty"`
	Membership string `json:"membership"`
	Power      int    `json:"power"`
}

func (a *Account) Members(ctx context.Context, roomID string) ([]MemberInfo, error) {
	if err := a.ensureMembers(ctx, roomID); err != nil {
		a.log.Warn().Err(err).Msg("No se pudo obtener la lista completa de miembros")
	}
	var out []MemberInfo
	for _, m := range a.store.StateOfType(roomID, "m.room.member") {
		c := gjson.ParseBytes(m.Content)
		ms := c.Get("membership").Str
		if ms == "leave" {
			continue
		}
		out = append(out, MemberInfo{ID: m.StateKey, Name: c.Get("displayname").Str, Avatar: c.Get("avatar_url").Str,
			Membership: ms, Power: a.powerLevel(roomID, m.StateKey)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Power != out[j].Power {
			return out[i].Power > out[j].Power
		}
		ni, nj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if ni == "" {
			ni = out[i].ID
		}
		if nj == "" {
			nj = out[j].ID
		}
		return ni < nj
	})
	return out, nil
}

func (a *Account) SetTag(ctx context.Context, roomID, tag string, on bool) error {
	var err error
	if on {
		err = a.cli.AddTag(ctx, id.RoomID(roomID), event.RoomTag(tag), 0.5)
		// Favorito y baja prioridad son excluyentes.
		other := map[string]string{"m.favourite": "m.lowpriority", "m.lowpriority": "m.favourite"}[tag]
		if other != "" {
			_ = a.cli.RemoveTag(ctx, id.RoomID(roomID), event.RoomTag(other))
		}
	} else {
		err = a.cli.RemoveTag(ctx, id.RoomID(roomID), event.RoomTag(tag))
	}
	return friendlyErrorNil(err)
}

// SetNotifyMode: "default", "all", "mentions", "mute".
func (a *Account) SetNotifyMode(ctx context.Context, roomID, mode string) error {
	_ = a.cli.DeletePushRule(ctx, "global", pushrules.RoomRule, roomID)
	_ = a.cli.DeletePushRule(ctx, "global", pushrules.OverrideRule, roomID)
	var err error
	switch mode {
	case "all":
		err = a.cli.PutPushRule(ctx, "global", pushrules.RoomRule, roomID, &mautrix.ReqPutPushRule{
			Actions: []*pushrules.PushAction{{Action: pushrules.ActionNotify}},
		})
	case "mentions":
		err = a.cli.PutPushRule(ctx, "global", pushrules.RoomRule, roomID, &mautrix.ReqPutPushRule{
			Actions: []*pushrules.PushAction{},
		})
	case "mute":
		err = a.cli.PutPushRule(ctx, "global", pushrules.OverrideRule, roomID, &mautrix.ReqPutPushRule{
			Actions:    []*pushrules.PushAction{},
			Conditions: []*pushrules.PushCondition{{Kind: pushrules.KindEventMatch, Key: "room_id", Pattern: roomID}},
		})
	}
	return friendlyErrorNil(err)
}

func (a *Account) Ignore(ctx context.Context, userID string, ignore bool) error {
	var c map[string]map[string]any
	if raw := a.store.GetAccountData("m.ignored_user_list"); raw != nil {
		_ = json.Unmarshal(raw, &c)
	}
	if c == nil {
		c = map[string]map[string]any{}
	}
	if c["ignored_users"] == nil {
		c["ignored_users"] = map[string]any{}
	}
	if ignore {
		c["ignored_users"][userID] = map[string]any{}
	} else {
		delete(c["ignored_users"], userID)
	}
	return friendlyErrorNil(a.cli.SetAccountData(ctx, "m.ignored_user_list", c))
}

func (a *Account) SetProfile(ctx context.Context, name *string, avatar *string) error {
	if name != nil {
		if err := a.cli.SetDisplayName(ctx, *name); err != nil {
			return friendlyError(err)
		}
	}
	if avatar != nil {
		uri, _ := id.ParseContentURI(*avatar)
		if err := a.cli.SetAvatarURL(ctx, uri); err != nil {
			return friendlyError(err)
		}
	}
	go a.refreshProfile()
	return nil
}

func (a *Account) SetPresence(ctx context.Context, presence, status string) error {
	return a.cli.SetPresence(ctx, mautrix.ReqPresence{Presence: event.Presence(presence), StatusMsg: status})
}

// --- espacios y directorio ---

func (a *Account) Hierarchy(ctx context.Context, roomID, from string) (*mautrix.RespHierarchy, error) {
	resp, err := a.cli.Hierarchy(ctx, id.RoomID(roomID), &mautrix.ReqHierarchy{From: from, Limit: 50, MaxDepth: ptr(1)})
	return resp, friendlyErrorNil(err)
}

func (a *Account) SpaceChild(ctx context.Context, space, child string, add bool, suggested bool) error {
	var content map[string]any
	if add {
		content = map[string]any{"via": []string{id.RoomID(child).String()[strings.LastIndex(child, ":")+1:]}, "suggested": suggested}
	} else {
		content = map[string]any{}
	}
	_, err := a.cli.SendStateEvent(ctx, id.RoomID(space), event.StateSpaceChild, child, content)
	if err == nil && add {
		// Intentamos marcar el padre en la sala hija (si tenemos permisos).
		_, _ = a.cli.SendStateEvent(ctx, id.RoomID(child), event.StateSpaceParent, space,
			map[string]any{"via": []string{space[strings.LastIndex(space, ":")+1:]}})
	}
	return friendlyErrorNil(err)
}

func (a *Account) PublicRooms(ctx context.Context, server, query, since string) (json.RawMessage, error) {
	body := map[string]any{"limit": 30}
	if since != "" {
		body["since"] = since
	}
	if query != "" {
		body["filter"] = map[string]any{"generic_search_term": query}
	}
	q := map[string]string{}
	if server != "" {
		q["server"] = server
	}
	u := a.cli.BuildURLWithQuery(mautrix.ClientURLPath{"v3", "publicRooms"}, q)
	var out json.RawMessage
	_, err := a.cli.MakeRequest(ctx, "POST", u, body, &out)
	return out, friendlyErrorNil(err)
}

func (a *Account) SearchUsers(ctx context.Context, query string) (*mautrix.RespSearchUserDirectory, error) {
	resp, err := a.cli.SearchUserDirectory(ctx, query, 20)
	return resp, friendlyErrorNil(err)
}

func (a *Account) RoomPreview(ctx context.Context, target string, via []string) (any, error) {
	resp, err := a.cli.GetRoomSummary(ctx, target, via...)
	return resp, friendlyErrorNil(err)
}

// ServerSearch busca en salas no cifradas del servidor (complementa la búsqueda local).
func (a *Account) ServerSearch(ctx context.Context, query, roomID string) ([]SearchHit, error) {
	filter := &mautrix.FilterPart{}
	if roomID != "" {
		filter.Rooms = []id.RoomID{id.RoomID(roomID)}
	}
	resp, err := a.cli.Search(ctx, &mautrix.ReqSearch{SearchTerm: query, Filter: filter, OrderBy: "recent"})
	if err != nil {
		return nil, err
	}
	var out []SearchHit
	for _, r := range resp.Results {
		evt := r.Event
		if evt == nil {
			continue
		}
		out = append(out, SearchHit{RoomID: evt.RoomID.String(), EventID: evt.ID.String(), Sender: evt.Sender.String(),
			TS: evt.Timestamp, Snippet: truncateRunes(gjson.GetBytes(rawContent(evt), "body").Str, 200)})
	}
	return out, nil
}

// RedactRecent borra (redacta) los últimos mensajes de un usuario en una sala: moderación rápida
// ante spam o abuso. Va de a uno con pausa para no chocar con el límite de pedidos del servidor.
func (a *Account) RedactRecent(ctx context.Context, roomID, user string, limit int, reason string) (int, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	// Traer historia reciente para no dejar mensajes sin ver.
	for i := 0; i < 5; i++ {
		if n, err := a.backfill(ctx, roomID); err != nil || n == 0 {
			break
		}
	}
	rows := a.store.RecentBySender(roomID, user, limit)
	done := 0
	for _, r := range rows {
		if strings.HasPrefix(r.EventID, "~") {
			continue
		}
		if err := a.Redact(ctx, roomID, r.EventID, reason); err != nil {
			if done == 0 {
				return 0, err
			}
			break
		}
		done++
		a.m.Broadcast(Push{Ev: "moderation", A: a.UserID(), D: map[string]any{"room": roomID, "user": user, "done": done, "total": len(rows)}})
		select {
		case <-ctx.Done():
			return done, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return done, nil
}
