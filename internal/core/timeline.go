package core

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/tidwall/gjson"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type Reaction struct {
	Key     string   `json:"key"`
	Count   int      `json:"count"`
	Mine    string   `json:"mine,omitempty"`
	Senders []string `json:"senders,omitempty"`
	URL     string   `json:"url,omitempty"` // reacciones con emoji personalizado (mxc)
}

type ThreadSum struct {
	Count        int    `json:"count"`
	LatestTS     int64  `json:"latest_ts,omitempty"`
	LatestSender string `json:"latest_sender,omitempty"`
	LatestBody   string `json:"latest_body,omitempty"`
	Mine         bool   `json:"mine,omitempty"`
}

type PollAnswer struct {
	ID    string `json:"id"`
	Votes int    `json:"votes"`
}

type PollAgg struct {
	Votes  map[string][]string `json:"votes"` // usuario -> respuestas
	Mine   []string            `json:"mine,omitempty"`
	Ended  bool                `json:"ended,omitempty"`
	Totals map[string]int      `json:"totals"`
}

type ReplyPreview struct {
	ID       string `json:"id"`
	Sender   string `json:"sender"`
	Name     string `json:"name,omitempty"`
	Type     string `json:"type"`
	MsgType  string `json:"msgtype,omitempty"`
	Body     string `json:"body,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
	UTD      bool   `json:"utd,omitempty"`
	Missing  bool   `json:"missing,omitempty"`
}

type Item struct {
	ID        string          `json:"id"`
	Txn       string          `json:"txn,omitempty"`
	Room      string          `json:"room"`
	Sender    string          `json:"sender"`
	Type      string          `json:"type"`
	SK        *string         `json:"sk,omitempty"`
	TS        int64           `json:"ts"`
	Sort      int64           `json:"sort"`
	Content   json.RawMessage `json:"content"`
	Prev      json.RawMessage `json:"prev,omitempty"`
	Edited    int64           `json:"edited,omitempty"`
	Redacted  bool            `json:"redacted,omitempty"`
	Reactions []Reaction      `json:"reactions,omitempty"`
	ReplyTo   string          `json:"reply_to,omitempty"`
	Reply     *ReplyPreview   `json:"reply,omitempty"`
	Thread    *ThreadSum      `json:"thread,omitempty"`
	InThread  string          `json:"in_thread,omitempty"`
	Enc       bool            `json:"enc,omitempty"`
	Trust     string          `json:"trust,omitempty"`
	UTD       string          `json:"utd,omitempty"`
	Status    string          `json:"status,omitempty"`
	Poll      *PollAgg        `json:"poll,omitempty"`
	RelType   string          `json:"rel_type,omitempty"`
	RelTo     string          `json:"rel_to,omitempty"`
}

// buildItems convierte filas de la base en elementos de interfaz con sus agregaciones.
func (a *Account) buildItems(roomID string, rows []*EventRow) ([]Item, map[string]Profile) {
	me := a.UserID()
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if !strings.HasPrefix(r.EventID, "~") {
			ids = append(ids, r.EventID)
		}
	}
	rels := map[string][]*EventRow{}
	for _, rel := range a.store.Relations(ids) {
		rels[rel.RelTo] = append(rels[rel.RelTo], rel)
	}
	threads := a.store.ThreadSummaries(ids)
	profiles := map[string]Profile{}
	addProfile := func(u string) {
		if _, ok := profiles[u]; !ok && u != "" {
			profiles[u] = a.memberProfile(roomID, u)
		}
	}
	items := make([]Item, 0, len(rows))
	for _, r := range rows {
		if a.isIgnored(r.Sender) && r.StateKey == nil {
			continue
		}
		it := Item{
			ID: r.EventID, Txn: r.TxnID, Room: roomID, Sender: r.Sender, Type: r.Type, SK: r.StateKey,
			TS: r.TS, Sort: r.Sort, Content: r.Content, ReplyTo: r.ReplyTo, InThread: r.ThreadRoot,
			Enc: len(r.Encrypted) > 0, Trust: r.Trust, UTD: r.DecryptError, Status: r.Status,
			RelType: r.RelType, RelTo: r.RelTo,
		}
		if it.UTD != "" {
			it.UTD = utdReason(it.UTD)
		}
		addProfile(r.Sender)
		if r.StateKey != nil {
			if pc := gjson.GetBytes(r.Unsigned, "prev_content"); pc.Exists() {
				it.Prev = json.RawMessage(pc.Raw)
			}
			if r.Type == "m.room.member" {
				addProfile(*r.StateKey)
			}
		}
		if r.RedactedBy != "" {
			it.Redacted = true
			it.Content = json.RawMessage("{}")
		}
		if !it.Redacted {
			a.applyRelations(&it, r, rels[r.EventID], me)
			if ts, ok := threads[r.EventID]; ok && ts.Count > 0 {
				sum := &ThreadSum{Count: ts.Count}
				if latest := a.store.GetEvent(ts.LatestID); latest != nil {
					sum.LatestTS, sum.LatestSender = latest.TS, latest.Sender
					sum.LatestBody = shortBody(latest)
					addProfile(latest.Sender)
				}
				it.Thread = sum
			}
			// Resumen de hilo provisto por el servidor (si tiene más respuestas que las locales).
			if th := gjson.GetBytes(r.Unsigned, `m\.relations.m\.thread`); th.Exists() {
				cnt := int(th.Get("count").Int())
				if it.Thread == nil || cnt > it.Thread.Count {
					if it.Thread == nil {
						it.Thread = &ThreadSum{}
					}
					it.Thread.Count = cnt
					it.Thread.Mine = th.Get("current_user_participated").Bool()
				}
			}
		}
		if it.ReplyTo != "" {
			it.Reply = a.replyPreview(roomID, it.ReplyTo)
			if it.Reply != nil {
				addProfile(it.Reply.Sender)
			}
		}
		items = append(items, it)
	}
	return items, profiles
}

func utdReason(err string) string {
	switch {
	case strings.Contains(err, "no session found"), strings.Contains(err, "failed to find session"), strings.Contains(err, "no session"):
		return "missing_keys"
	case strings.Contains(err, "withheld"):
		return "withheld"
	case strings.Contains(err, "unverified"), strings.Contains(err, "insecure"):
		return "unverified_device"
	case strings.Contains(err, "message index"), strings.Contains(err, "ratchet"):
		return "index"
	default:
		return "error"
	}
}

func shortBody(e *EventRow) string {
	if e.RedactedBy != "" || e.DecryptError != "" {
		return ""
	}
	b := gjson.GetBytes(e.Content, "body").Str
	if r := []rune(b); len(r) > 140 {
		b = string(r[:140]) + "…"
	}
	return b
}

func (a *Account) replyPreview(roomID, eventID string) *ReplyPreview {
	e := a.store.GetEvent(eventID)
	if e == nil {
		return &ReplyPreview{ID: eventID, Missing: true}
	}
	p := &ReplyPreview{ID: eventID, Sender: e.Sender, Type: e.Type, Redacted: e.RedactedBy != "", UTD: e.DecryptError != ""}
	p.Name = a.memberProfile(roomID, e.Sender).Name
	if !p.Redacted && !p.UTD {
		content := gjson.ParseBytes(e.Content)
		// Si fue editado, mostrar la última versión.
		for _, rel := range a.store.Relations([]string{eventID}) {
			if rel.RelType == "m.replace" && rel.Sender == e.Sender {
				if nc := gjson.GetBytes(rel.Content, `m\.new_content`); nc.Exists() {
					content = nc
				}
			}
		}
		p.MsgType = content.Get("msgtype").Str
		body := content.Get("body").Str
		if r := []rune(body); len(r) > 200 {
			body = string(r[:200]) + "…"
		}
		p.Body = body
	}
	return p
}

// applyRelations aplica ediciones, reacciones y votos de encuestas.
func (a *Account) applyRelations(it *Item, orig *EventRow, rels []*EventRow, me string) {
	var lastEdit *EventRow
	reactions := map[string]*Reaction{}
	var order []string
	var pollResponses []*EventRow
	var pollEnd *EventRow
	for _, rel := range rels {
		switch {
		case rel.RelType == "m.replace" && rel.Sender == orig.Sender && rel.DecryptError == "":
			if lastEdit == nil || rel.TS > lastEdit.TS || (rel.TS == lastEdit.TS && rel.Sort > lastEdit.Sort) {
				lastEdit = rel
			}
		case rel.RelType == "m.annotation":
			key := rel.RelKey
			if key == "" {
				continue
			}
			rx, ok := reactions[key]
			if !ok {
				rx = &Reaction{Key: key}
				if strings.HasPrefix(key, "mxc://") {
					rx.URL = key
				}
				reactions[key] = rx
				order = append(order, key)
			}
			dup := false
			for _, s := range rx.Senders {
				if s == rel.Sender {
					dup = true
				}
			}
			if dup {
				continue
			}
			rx.Count++
			rx.Senders = append(rx.Senders, rel.Sender)
			if rel.Sender == me {
				rx.Mine = rel.EventID
			}
		case rel.RelType == "m.reference" && (rel.Type == "org.matrix.msc3381.poll.response" || rel.Type == "m.poll.response"):
			pollResponses = append(pollResponses, rel)
		case rel.RelType == "m.reference" && (rel.Type == "org.matrix.msc3381.poll.end" || rel.Type == "m.poll.end"):
			if rel.Sender == orig.Sender || a.powerLevel(orig.RoomID, rel.Sender) >= 50 {
				if pollEnd == nil || rel.TS < pollEnd.TS {
					pollEnd = rel
				}
			}
		}
	}
	if lastEdit != nil {
		nc := gjson.GetBytes(lastEdit.Content, `m\.new_content`)
		if nc.Exists() && nc.IsObject() {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(nc.Raw), &m) == nil {
				// La relación original (respuesta/hilo) se conserva.
				if rel, ok := gjsonRaw(orig.Content, `m\.relates_to`); ok {
					m["m.relates_to"] = rel
				}
				it.Content, _ = json.Marshal(m)
				it.Edited = lastEdit.TS
			}
		}
	}
	for _, k := range order {
		it.Reactions = append(it.Reactions, *reactions[k])
	}
	sort.SliceStable(it.Reactions, func(i, j int) bool { return it.Reactions[i].Count > it.Reactions[j].Count })
	if strings.HasSuffix(orig.Type, "poll.start") {
		it.Poll = tallyPoll(orig, pollResponses, pollEnd, me)
	}
}

func gjsonRaw(data json.RawMessage, path string) (json.RawMessage, bool) {
	r := gjson.GetBytes(data, path)
	if !r.Exists() {
		return nil, false
	}
	return json.RawMessage(r.Raw), true
}

func tallyPoll(start *EventRow, responses []*EventRow, end *EventRow, me string) *PollAgg {
	c := gjson.ParseBytes(start.Content)
	maxSel := int(c.Get(`org\.matrix\.msc3381\.poll\.start.max_selections`).Int())
	if maxSel == 0 {
		maxSel = int(c.Get(`m\.poll.max_selections`).Int())
	}
	if maxSel <= 0 {
		maxSel = 1
	}
	valid := map[string]bool{}
	for _, ans := range c.Get(`org\.matrix\.msc3381\.poll\.start.answers`).Array() {
		valid[ans.Get("id").Str] = true
	}
	for _, ans := range c.Get(`m\.poll.answers`).Array() {
		valid[ans.Get(`m\.id`).Str] = true
	}
	latest := map[string]*EventRow{}
	for _, r := range responses {
		if end != nil && r.TS > end.TS {
			continue
		}
		if prev, ok := latest[r.Sender]; !ok || r.TS > prev.TS {
			latest[r.Sender] = r
		}
	}
	agg := &PollAgg{Votes: map[string][]string{}, Totals: map[string]int{}, Ended: end != nil}
	for user, r := range latest {
		rc := gjson.ParseBytes(r.Content)
		answers := rc.Get(`org\.matrix\.msc3381\.poll\.response.answers`).Array()
		if len(answers) == 0 {
			answers = rc.Get(`m\.selections`).Array()
		}
		var sel []string
		for _, ans := range answers {
			if valid[ans.Str] && len(sel) < maxSel {
				sel = append(sel, ans.Str)
			}
		}
		if len(sel) == 0 {
			continue
		}
		agg.Votes[user] = sel
		for _, s := range sel {
			agg.Totals[s]++
		}
		if user == me {
			agg.Mine = sel
		}
	}
	return agg
}

// --- API de línea de tiempo ---

type TimelinePage struct {
	Items    []Item              `json:"items"`
	Profiles map[string]Profile  `json:"profiles"`
	HasMore  bool                `json:"has_more"`
	Receipts map[string][]string `json:"receipts,omitempty"`
}

// Timeline devuelve la página anterior a "before" (sort), paginando del servidor si hace falta.
func (a *Account) Timeline(ctx context.Context, roomID string, before int64, limit int) (*TimelinePage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows := a.store.TimelineBefore(roomID, before, limit)
	// Si no alcanza, pedimos más historia al servidor (hasta 3 rondas).
	for round := 0; len(rows) < limit && round < 3; round++ {
		got, err := a.backfill(ctx, roomID)
		if err != nil {
			if len(rows) > 0 {
				break
			}
			return nil, err
		}
		if got == 0 {
			break
		}
		rows = a.store.TimelineBefore(roomID, before, limit)
	}
	items, profs := a.buildItems(roomID, rows)
	r := a.store.GetRoom(roomID)
	page := &TimelinePage{Items: items, Profiles: profs}
	if r != nil {
		page.HasMore = !r.HistoryComplete || len(rows) >= limit
	}
	if before <= 0 {
		page.Receipts = a.store.Receipts(roomID)
	}
	return page, nil
}

var backfillLocks = newKeyedMutex()

// backfill trae una tanda de historia con /messages y la guarda (con sort decreciente).
func (a *Account) backfill(ctx context.Context, roomID string) (int, error) {
	unlock := backfillLocks.Lock(a.UserID() + roomID)
	defer unlock()
	r := a.store.GetRoom(roomID)
	if r == nil {
		return 0, errors.New("sala desconocida")
	}
	if r.HistoryComplete {
		return 0, nil
	}
	if r.PrevBatch == "" {
		// Sin token: tomamos el más reciente para arrancar.
		a.store.UpdateRoom(roomID, map[string]any{"history_complete": 1})
		return 0, nil
	}
	filter := &mautrix.FilterPart{LazyLoadMembers: true}
	resp, err := a.cli.Messages(ctx, id.RoomID(roomID), r.PrevBatch, "", mautrix.DirectionBackward, filter, 60)
	if err != nil {
		return 0, err
	}
	for _, evt := range resp.State {
		a.putStateEvent(roomID, evt)
	}
	minSort := a.store.MinSort(roomID)
	if minSort == 0 {
		minSort = a.store.NextSort()
	}
	batch := newSyncBatch()
	added := 0
	for _, evt := range resp.Chunk {
		pe := a.prepareEvent(ctx, roomID, evt)
		if a.store.GetEvent(pe.row.EventID) != nil {
			continue
		}
		minSort--
		pe.row.Sort = minSort
		for _, b := range a.bundledRelations(ctx, roomID, evt) {
			b.row.Sort = minSort
			a.ingestRow(b.row, b.redacts, batch)
		}
		if a.ingestRow(pe.row, pe.redacts, batch) {
			added++
		}
	}
	fields := map[string]any{"prev_batch": resp.End}
	if resp.End == "" || len(resp.Chunk) == 0 {
		fields["history_complete"] = 1
	}
	a.store.UpdateRoom(roomID, fields)
	// Ediciones/reacciones que llegaron en la historia actualizan elementos ya visibles.
	for _, ids := range batch.updated {
		if len(ids) > 0 {
			bb := newSyncBatch()
			bb.updated = batch.updated
			a.flushBatch(bb)
			break
		}
	}
	return added, nil
}

// FetchEvent obtiene un evento puntual (respuestas a mensajes fuera de la caché).
func (a *Account) FetchEvent(ctx context.Context, roomID, eventID string) (*Item, map[string]Profile, error) {
	if e := a.store.GetEvent(eventID); e != nil {
		items, profs := a.buildItems(roomID, []*EventRow{e})
		if len(items) > 0 {
			return &items[0], profs, nil
		}
	}
	evt, err := a.cli.GetEvent(ctx, id.RoomID(roomID), id.EventID(eventID))
	if err != nil {
		return nil, nil, err
	}
	pe := a.prepareEvent(ctx, roomID, evt)
	items, profs := a.buildItems(roomID, []*EventRow{pe.row})
	if len(items) == 0 {
		return nil, nil, errors.New("evento no disponible")
	}
	return &items[0], profs, nil
}

// Thread devuelve las respuestas de un hilo (trae del servidor lo que falte).
func (a *Account) Thread(ctx context.Context, roomID, root string) (*TimelinePage, error) {
	from := ""
	for round := 0; round < 5; round++ {
		resp, err := a.cli.GetRelations(ctx, id.RoomID(roomID), id.EventID(root), &mautrix.ReqGetRelations{
			RelationType: event.RelThread, Dir: mautrix.DirectionBackward, Limit: 100, From: from,
		})
		if err != nil {
			break
		}
		batch := newSyncBatch()
		for _, evt := range resp.Chunk {
			pe := a.prepareEvent(ctx, roomID, evt)
			if a.store.GetEvent(pe.row.EventID) != nil {
				continue
			}
			pe.row.Sort = a.store.NextSort()
			a.ingestRow(pe.row, pe.redacts, batch)
		}
		if resp.NextBatch == "" {
			break
		}
		from = resp.NextBatch
	}
	rows := a.store.ThreadEvents(root, 500)
	var rootRow *EventRow
	if rootRow = a.store.GetEvent(root); rootRow == nil {
		if evt, err := a.cli.GetEvent(ctx, id.RoomID(roomID), id.EventID(root)); err == nil {
			pe := a.prepareEvent(ctx, roomID, evt)
			rootRow = pe.row
		}
	}
	if rootRow != nil {
		rows = append([]*EventRow{rootRow}, rows...)
	}
	items, profs := a.buildItems(roomID, rows)
	return &TimelinePage{Items: items, Profiles: profs}, nil
}

// ThreadList usa la API /threads del servidor.
func (a *Account) ThreadList(ctx context.Context, roomID string) (*TimelinePage, error) {
	var resp struct {
		Chunk     []*event.Event `json:"chunk"`
		NextBatch string         `json:"next_batch"`
	}
	u := a.cli.BuildURLWithQuery(mautrix.ClientURLPath{"v1", "rooms", roomID, "threads"}, map[string]string{"limit": "50"})
	if _, err := a.cli.MakeRequest(ctx, "GET", u, nil, &resp); err != nil {
		return nil, err
	}
	var rows []*EventRow
	batch := newSyncBatch()
	for _, evt := range resp.Chunk {
		pe := a.prepareEvent(ctx, roomID, evt)
		if ex := a.store.GetEvent(pe.row.EventID); ex != nil {
			pe.row = ex
		}
		// La raíz no se guarda: insertarla fuera de orden rompería la línea de tiempo.
		for _, b := range a.bundledRelations(ctx, roomID, evt) {
			if a.store.GetEvent(b.row.EventID) == nil {
				a.ingestRow(b.row, b.redacts, batch)
			}
		}
		rows = append(rows, pe.row)
	}
	items, profs := a.buildItems(roomID, rows)
	return &TimelinePage{Items: items, Profiles: profs}, nil
}
