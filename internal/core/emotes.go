package core

import (
	"context"
	"encoding/json"

	"github.com/tidwall/gjson"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Paquetes de emojis y stickers (MSC2545). Se leen las variantes estable e inestable.

type EmotePack struct {
	ID       string          `json:"id"`
	Source   string          `json:"source"` // user | room
	Room     string          `json:"room,omitempty"`
	StateKey string          `json:"state_key,omitempty"`
	Name     string          `json:"name"`
	Avatar   string          `json:"avatar,omitempty"`
	Usage    []string        `json:"usage,omitempty"`
	Images   json.RawMessage `json:"images"`
	Global   bool            `json:"global,omitempty"`
	CanEdit  bool            `json:"can_edit"`
	Raw      json.RawMessage `json:"raw"`
}

func packFrom(id, source, room, sk string, content json.RawMessage) EmotePack {
	c := gjson.ParseBytes(content)
	p := EmotePack{ID: id, Source: source, Room: room, StateKey: sk, Raw: content}
	p.Name = c.Get("pack.display_name").Str
	p.Avatar = c.Get("pack.avatar_url").Str
	for _, u := range c.Get("pack.usage").Array() {
		p.Usage = append(p.Usage, u.Str)
	}
	imgs := c.Get("images")
	if !imgs.Exists() {
		// Formato antiguo: "emoticons" o "short".
		if e := c.Get("emoticons"); e.Exists() {
			imgs = e
		}
	}
	if imgs.Exists() {
		p.Images = json.RawMessage(imgs.Raw)
	} else {
		p.Images = json.RawMessage("{}")
	}
	return p
}

func (a *Account) EmotePacks(roomID string) []EmotePack {
	var out []EmotePack
	for _, typ := range []string{"m.image_pack", "im.ponies.user_emotes"} {
		if c := a.store.GetAccountData(typ); c != nil && len(gjson.GetBytes(c, "images").Map()) > 0 {
			out = append(out, packFrom("user:"+typ, "user", "", typ, c))
			break
		}
	}
	if len(out) == 0 {
		out = append(out, EmotePack{ID: "user:im.ponies.user_emotes", Source: "user", StateKey: "im.ponies.user_emotes",
			Images: json.RawMessage("{}"), Raw: json.RawMessage("{}")})
	}
	out[0].CanEdit = true
	seen := map[string]bool{}
	addRoomPacks := func(rid string, only map[string]bool, global bool) {
		for _, typ := range []string{"m.room.image_pack", "im.ponies.room_emotes"} {
			for _, st := range a.store.StateOfType(rid, typ) {
				if only != nil && !only[st.StateKey] {
					continue
				}
				key := rid + "|" + st.StateKey
				if seen[key] || len(gjson.GetBytes(st.Content, "images").Map()) == 0 {
					continue
				}
				seen[key] = true
				p := packFrom("room:"+rid+":"+typ+":"+st.StateKey, "room", rid, st.StateKey, st.Content)
				if p.Name == "" {
					if s := a.RoomSummary(rid); s != nil {
						p.Name = s.Name
					}
				}
				p.Global = global
				p.CanEdit = a.powerLevel(rid, a.UserID()) >= 50
				out = append(out, p)
			}
		}
	}
	if roomID != "" {
		addRoomPacks(roomID, nil, false)
	}
	for _, typ := range []string{"m.image_pack.rooms", "im.ponies.emote_rooms"} {
		c := a.store.GetAccountData(typ)
		if c == nil {
			continue
		}
		for rid, keys := range gjson.GetBytes(c, "rooms").Map() {
			only := map[string]bool{}
			for k := range keys.Map() {
				only[k] = true
			}
			addRoomPacks(rid, only, true)
		}
	}
	return out
}

// SaveEmotePack guarda un paquete propio (account data) o de sala (estado).
func (a *Account) SaveEmotePack(ctx context.Context, roomID, stateKey string, content json.RawMessage) error {
	var c map[string]any
	if err := json.Unmarshal(content, &c); err != nil {
		return err
	}
	if roomID == "" {
		typ := "im.ponies.user_emotes"
		if err := a.cli.SetAccountData(ctx, typ, c); err != nil {
			return friendlyError(err)
		}
		a.store.PutAccountData(typ, content)
		return nil
	}
	_, err := a.cli.SendStateEvent(ctx, id.RoomID(roomID), event.Type{Type: "im.ponies.room_emotes", Class: event.StateEventType}, stateKey, c)
	if err == nil {
		a.store.PutState(roomID, StateEntry{Type: "im.ponies.room_emotes", StateKey: stateKey, Content: content})
	}
	return friendlyErrorNil(err)
}

// SetPackGlobal activa un paquete de sala en todas las conversaciones.
func (a *Account) SetPackGlobal(ctx context.Context, roomID, stateKey string, on bool) error {
	typ := "im.ponies.emote_rooms"
	var c map[string]map[string]map[string]any
	if raw := a.store.GetAccountData(typ); raw != nil {
		_ = json.Unmarshal(raw, &c)
	}
	if c == nil {
		c = map[string]map[string]map[string]any{}
	}
	if c["rooms"] == nil {
		c["rooms"] = map[string]map[string]any{}
	}
	if on {
		if c["rooms"][roomID] == nil {
			c["rooms"][roomID] = map[string]any{}
		}
		c["rooms"][roomID][stateKey] = map[string]any{}
	} else if c["rooms"][roomID] != nil {
		delete(c["rooms"][roomID], stateKey)
		if len(c["rooms"][roomID]) == 0 {
			delete(c["rooms"], roomID)
		}
	}
	if err := a.cli.SetAccountData(ctx, typ, c); err != nil {
		return friendlyError(err)
	}
	b, _ := json.Marshal(c)
	a.store.PutAccountData(typ, b)
	return nil
}

// UploadPlain sube sin cifrar (necesario para emojis/stickers/avatares).
func (a *Account) UploadPlain(ctx context.Context, data []byte, mime, name string) (string, error) {
	url, _, err := a.uploadBytes(ctx, data, mime, name, false)
	return url, err
}
