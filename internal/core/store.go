package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	_ "go.mau.fi/util/dbutil/litestream" // registra el driver "sqlite3-fk-wal"
)

// Store es la base local de cada cuenta: caché de salas, línea de tiempo,
// estado, índice de búsqueda (incluye mensajes cifrados ya descifrados),
// borradores y mensajes programados.
type Store struct {
	db   *sql.DB // escrituras (una sola conexión)
	rd   *sql.DB // lecturas: varias conexiones que no esperan a las escrituras (WAL)
	sort atomic.Int64
	mu   sync.Mutex // serializa escrituras compuestas
}

const schema = `
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS rooms (
	room_id TEXT PRIMARY KEY,
	membership TEXT NOT NULL DEFAULT 'join',
	prev_batch TEXT NOT NULL DEFAULT '',
	history_complete INTEGER NOT NULL DEFAULT 0,
	heroes TEXT NOT NULL DEFAULT '[]',
	joined_count INTEGER NOT NULL DEFAULT 0,
	invited_count INTEGER NOT NULL DEFAULT 0,
	tags TEXT NOT NULL DEFAULT '{}',
	read_sort INTEGER NOT NULL DEFAULT 0,
	read_event TEXT NOT NULL DEFAULT '',
	marked_unread INTEGER NOT NULL DEFAULT 0,
	srv_notif INTEGER NOT NULL DEFAULT 0,
	srv_highlight INTEGER NOT NULL DEFAULT 0,
	local_highlight INTEGER NOT NULL DEFAULT 0,
	bump_ts INTEGER NOT NULL DEFAULT 0,
	members_loaded INTEGER NOT NULL DEFAULT 0,
	sss_name TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS state (
	room_id TEXT NOT NULL,
	type TEXT NOT NULL,
	state_key TEXT NOT NULL,
	event_id TEXT NOT NULL DEFAULT '',
	sender TEXT NOT NULL DEFAULT '',
	ts INTEGER NOT NULL DEFAULT 0,
	content TEXT NOT NULL,
	PRIMARY KEY (room_id, type, state_key)
);
CREATE TABLE IF NOT EXISTS events (
	rowid INTEGER PRIMARY KEY,
	room_id TEXT NOT NULL,
	event_id TEXT NOT NULL UNIQUE,
	txn_id TEXT NOT NULL DEFAULT '',
	sender TEXT NOT NULL,
	type TEXT NOT NULL,
	state_key TEXT,
	ts INTEGER NOT NULL,
	sort INTEGER NOT NULL,
	content TEXT NOT NULL,
	unsigned TEXT NOT NULL DEFAULT '{}',
	rel_type TEXT NOT NULL DEFAULT '',
	rel_to TEXT NOT NULL DEFAULT '',
	rel_key TEXT NOT NULL DEFAULT '',
	thread_root TEXT NOT NULL DEFAULT '',
	reply_to TEXT NOT NULL DEFAULT '',
	redacted_by TEXT NOT NULL DEFAULT '',
	encrypted TEXT NOT NULL DEFAULT '',
	decrypt_error TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	trust TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS events_room_sort ON events(room_id, sort);
CREATE INDEX IF NOT EXISTS events_rel ON events(rel_to) WHERE rel_to != '';
CREATE INDEX IF NOT EXISTS events_thread ON events(thread_root, sort) WHERE thread_root != '';
CREATE INDEX IF NOT EXISTS events_utd ON events(session_id) WHERE decrypt_error != '';
CREATE INDEX IF NOT EXISTS events_txn ON events(txn_id) WHERE txn_id != '';
CREATE VIRTUAL TABLE IF NOT EXISTS search USING fts5(
	body, room_id UNINDEXED, event_id UNINDEXED, sender UNINDEXED, ts UNINDEXED,
	tokenize = 'unicode61 remove_diacritics 2'
);
CREATE TABLE IF NOT EXISTS drafts (room_id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scheduled (
	id INTEGER PRIMARY KEY,
	room_id TEXT NOT NULL,
	send_at INTEGER NOT NULL,
	payload TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS account_data (type TEXT PRIMARY KEY, content TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS room_account_data (
	room_id TEXT NOT NULL, type TEXT NOT NULL, content TEXT NOT NULL,
	PRIMARY KEY (room_id, type)
);
CREATE TABLE IF NOT EXISTS receipts (
	room_id TEXT NOT NULL, user_id TEXT NOT NULL, event_id TEXT NOT NULL, ts INTEGER NOT NULL,
	PRIMARY KEY (room_id, user_id)
);
`

const sortBase = int64(1) << 40

// hiddenTypes no se muestran en la línea de tiempo principal (se agregan o son señalización).
const hiddenTypes = `'m.room.redaction','m.reaction','m.call.candidates','m.call.answer','m.call.select_answer',
	'm.call.negotiate','m.call.sdp_stream_metadata_changed','org.matrix.call.sdp_stream_metadata_changed',
	'm.call.reject','org.matrix.msc3401.call.member','m.call.member','im.ponies.room_emotes','m.room.image_pack',
	'org.matrix.msc3381.poll.response','m.poll.response','org.matrix.msc4075.rtc.notification.decline'`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite3-fk-wal", "file:"+path+"?_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: una conexión evita "database is locked"
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	// Lecturas aparte: con WAL leen en paralelo con la escritura y durante los checkpoints, así la
	// interfaz (abrir una sala, la lista, el conteo de no leídos) no espera a la sincronización.
	rd, err := sql.Open("sqlite3-fk-wal", "file:"+path+"?_txlock=deferred&_query_only=true")
	if err != nil {
		db.Close()
		return nil, err
	}
	rd.SetMaxOpenConns(4)
	rd.SetMaxIdleConns(4)
	s := &Store{db: db, rd: rd}
	var max sql.NullInt64
	_ = db.QueryRow(`SELECT MAX(sort) FROM events`).Scan(&max)
	if max.Valid && max.Int64 >= sortBase {
		s.sort.Store(max.Int64)
	} else {
		s.sort.Store(sortBase)
	}
	return s, nil
}

func (s *Store) Close() error {
	_ = s.rd.Close()
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return s.db.Close()
}

func (s *Store) NextSort() int64 { return s.sort.Add(1) }

// --- kv ---

func (s *Store) GetKV(k string) string {
	var v string
	_ = s.rd.QueryRow(`SELECT v FROM kv WHERE k=?`, k).Scan(&v)
	return v
}

func (s *Store) SetKV(k, v string) {
	_, _ = s.db.Exec(`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
}

// --- rooms ---

type roomRow struct {
	ID              string
	Membership      string
	PrevBatch       string
	HistoryComplete bool
	Heroes          []string
	Joined, Invited int
	Tags            map[string]json.RawMessage
	ReadSort        int64
	ReadEvent       string
	MarkedUnread    bool
	SrvNotif        int
	SrvHighlight    int
	LocalHighlight  int
	BumpTS          int64
	MembersLoaded   bool
	SSSName         string
}

const roomCols = `room_id, membership, prev_batch, history_complete, heroes, joined_count, invited_count,
	tags, read_sort, read_event, marked_unread, srv_notif, srv_highlight, local_highlight, bump_ts, members_loaded, sss_name`

func scanRoom(sc interface{ Scan(...any) error }) (*roomRow, error) {
	r := &roomRow{}
	var heroes, tags string
	err := sc.Scan(&r.ID, &r.Membership, &r.PrevBatch, &r.HistoryComplete, &heroes, &r.Joined, &r.Invited,
		&tags, &r.ReadSort, &r.ReadEvent, &r.MarkedUnread, &r.SrvNotif, &r.SrvHighlight, &r.LocalHighlight,
		&r.BumpTS, &r.MembersLoaded, &r.SSSName)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(heroes), &r.Heroes)
	_ = json.Unmarshal([]byte(tags), &r.Tags)
	return r, nil
}

func (s *Store) GetRoom(roomID string) *roomRow {
	r, err := scanRoom(s.rd.QueryRow(`SELECT `+roomCols+` FROM rooms WHERE room_id=?`, roomID))
	if err != nil {
		return nil
	}
	return r
}

func (s *Store) AllRooms() []*roomRow {
	rows, err := s.rd.Query(`SELECT ` + roomCols + ` FROM rooms WHERE membership != 'leave'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*roomRow
	for rows.Next() {
		if r, err := scanRoom(rows); err == nil {
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) EnsureRoom(roomID, membership string) {
	_, _ = s.db.Exec(`INSERT INTO rooms(room_id, membership) VALUES(?,?)
		ON CONFLICT(room_id) DO UPDATE SET membership=excluded.membership`, roomID, membership)
}

func (s *Store) UpdateRoom(roomID string, fields map[string]any) {
	if len(fields) == 0 {
		return
	}
	var sets []string
	var args []any
	for k, v := range fields {
		switch vv := v.(type) {
		case []string, map[string]json.RawMessage:
			b, _ := json.Marshal(vv)
			v = string(b)
		}
		sets = append(sets, k+"=?")
		args = append(args, v)
	}
	args = append(args, roomID)
	_, _ = s.db.Exec(`UPDATE rooms SET `+strings.Join(sets, ",")+` WHERE room_id=?`, args...)
}

// ForgetRoom borra todo lo local de una sala (al salir).
// No usa transacción propia: puede llamarse dentro de WithTx.
func (s *Store) ForgetRoom(roomID string) {
	for _, q := range []string{
		`DELETE FROM events WHERE room_id=?`,
		`DELETE FROM state WHERE room_id=?`,
		`DELETE FROM search WHERE room_id=?`,
		`DELETE FROM receipts WHERE room_id=?`,
		`DELETE FROM room_account_data WHERE room_id=?`,
		`DELETE FROM drafts WHERE room_id=?`,
		`DELETE FROM rooms WHERE room_id=?`,
	} {
		_, _ = s.db.Exec(q, roomID)
	}
}

// --- state ---

type StateEntry struct {
	Type     string          `json:"type"`
	StateKey string          `json:"state_key"`
	EventID  string          `json:"event_id,omitempty"`
	Sender   string          `json:"sender,omitempty"`
	TS       int64           `json:"origin_server_ts,omitempty"`
	Content  json.RawMessage `json:"content"`
}

func (s *Store) PutState(roomID string, e StateEntry) {
	if len(e.Content) == 0 {
		e.Content = json.RawMessage("{}")
	}
	_, _ = s.db.Exec(`INSERT INTO state(room_id,type,state_key,event_id,sender,ts,content) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(room_id,type,state_key) DO UPDATE SET event_id=excluded.event_id, sender=excluded.sender,
		ts=excluded.ts, content=excluded.content`,
		roomID, e.Type, e.StateKey, e.EventID, e.Sender, e.TS, string(e.Content))
}

func (s *Store) GetState(roomID, typ, stateKey string) *StateEntry {
	e := &StateEntry{Type: typ, StateKey: stateKey}
	var content string
	err := s.rd.QueryRow(`SELECT event_id, sender, ts, content FROM state WHERE room_id=? AND type=? AND state_key=?`,
		roomID, typ, stateKey).Scan(&e.EventID, &e.Sender, &e.TS, &content)
	if err != nil {
		return nil
	}
	e.Content = json.RawMessage(content)
	return e
}

func (s *Store) GetStateContent(roomID, typ, stateKey string, into any) bool {
	e := s.GetState(roomID, typ, stateKey)
	if e == nil {
		return false
	}
	return json.Unmarshal(e.Content, into) == nil
}

func (s *Store) StateOfType(roomID, typ string) []StateEntry {
	rows, err := s.rd.Query(`SELECT state_key, event_id, sender, ts, content FROM state WHERE room_id=? AND type=?`, roomID, typ)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []StateEntry
	for rows.Next() {
		e := StateEntry{Type: typ}
		var content string
		if rows.Scan(&e.StateKey, &e.EventID, &e.Sender, &e.TS, &content) == nil {
			e.Content = json.RawMessage(content)
			out = append(out, e)
		}
	}
	return out
}

func (s *Store) DeleteState(roomID, typ, stateKey string) {
	_, _ = s.db.Exec(`DELETE FROM state WHERE room_id=? AND type=? AND state_key=?`, roomID, typ, stateKey)
}

// --- events ---

type EventRow struct {
	RoomID       string
	EventID      string
	TxnID        string
	Sender       string
	Type         string
	StateKey     *string
	TS           int64
	Sort         int64
	Content      json.RawMessage
	Unsigned     json.RawMessage
	RelType      string
	RelTo        string
	RelKey       string
	ThreadRoot   string
	ReplyTo      string
	RedactedBy   string
	Encrypted    json.RawMessage
	DecryptError string
	SessionID    string
	Trust        string
	Status       string
}

const eventCols = `room_id, event_id, txn_id, sender, type, state_key, ts, sort, content, unsigned, rel_type, rel_to,
	rel_key, thread_root, reply_to, redacted_by, encrypted, decrypt_error, session_id, trust, status`

func scanEvent(sc interface{ Scan(...any) error }) (*EventRow, error) {
	e := &EventRow{}
	var sk sql.NullString
	var content, unsigned, enc string
	err := sc.Scan(&e.RoomID, &e.EventID, &e.TxnID, &e.Sender, &e.Type, &sk, &e.TS, &e.Sort, &content, &unsigned,
		&e.RelType, &e.RelTo, &e.RelKey, &e.ThreadRoot, &e.ReplyTo, &e.RedactedBy, &enc, &e.DecryptError,
		&e.SessionID, &e.Trust, &e.Status)
	if err != nil {
		return nil, err
	}
	if sk.Valid {
		v := sk.String
		e.StateKey = &v
	}
	e.Content = json.RawMessage(content)
	e.Unsigned = json.RawMessage(unsigned)
	if enc != "" {
		e.Encrypted = json.RawMessage(enc)
	}
	return e, nil
}

func nz(r json.RawMessage, def string) string {
	if len(r) == 0 {
		return def
	}
	return string(r)
}

// PutEvent inserta o actualiza un evento (por event_id). Devuelve el sort final.
func (s *Store) PutEvent(e *EventRow) int64 {
	var sk any
	if e.StateKey != nil {
		sk = *e.StateKey
	}
	var existing int64
	err := s.db.QueryRow(`SELECT sort FROM events WHERE event_id=?`, e.EventID).Scan(&existing)
	if err == nil {
		_, _ = s.db.Exec(`UPDATE events SET sender=?, type=?, state_key=?, ts=?, content=?, unsigned=?, rel_type=?,
			rel_to=?, rel_key=?, thread_root=?, reply_to=?, encrypted=?, decrypt_error=?, session_id=?, trust=?, status=?
			WHERE event_id=?`,
			e.Sender, e.Type, sk, e.TS, nz(e.Content, "{}"), nz(e.Unsigned, "{}"), e.RelType, e.RelTo, e.RelKey,
			e.ThreadRoot, e.ReplyTo, nz(e.Encrypted, ""), e.DecryptError, e.SessionID, e.Trust, e.Status, e.EventID)
		e.Sort = existing
		return existing
	}
	if e.Sort == 0 {
		e.Sort = s.NextSort()
	}
	_, err = s.db.Exec(`INSERT INTO events(`+eventCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.RoomID, e.EventID, e.TxnID, e.Sender, e.Type, sk, e.TS, e.Sort, nz(e.Content, "{}"), nz(e.Unsigned, "{}"),
		e.RelType, e.RelTo, e.RelKey, e.ThreadRoot, e.ReplyTo, e.RedactedBy, nz(e.Encrypted, ""), e.DecryptError,
		e.SessionID, e.Trust, e.Status)
	if err != nil {
		return 0
	}
	return e.Sort
}

func (s *Store) GetEvent(eventID string) *EventRow {
	e, err := scanEvent(s.rd.QueryRow(`SELECT `+eventCols+` FROM events WHERE event_id=?`, eventID))
	if err != nil {
		return nil
	}
	return e
}

func (s *Store) GetEventByTxn(txnID string) *EventRow {
	if txnID == "" {
		return nil
	}
	e, err := scanEvent(s.rd.QueryRow(`SELECT `+eventCols+` FROM events WHERE txn_id=?`, txnID))
	if err != nil {
		return nil
	}
	return e
}

// ReplaceLocalEcho convierte un eco local (~txn) en el evento real.
func (s *Store) ReplaceLocalEcho(txnID, eventID string, newSort int64) bool {
	res, err := s.db.Exec(`UPDATE events SET event_id=?, status='', sort=? WHERE txn_id=? AND event_id LIKE '~%'`,
		eventID, newSort, txnID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (s *Store) SetEventStatus(eventID, status string) {
	_, _ = s.db.Exec(`UPDATE events SET status=? WHERE event_id=?`, status, eventID)
}

func (s *Store) SetEventIDForTxn(txnID, eventID string) {
	// Si el eco del sync ya llegó, borramos el duplicado local.
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE event_id=?`, eventID).Scan(&n)
	if n > 0 {
		_, _ = s.db.Exec(`DELETE FROM events WHERE txn_id=? AND event_id LIKE '~%'`, txnID)
		return
	}
	_, _ = s.db.Exec(`UPDATE events SET event_id=?, status='' WHERE txn_id=? AND event_id LIKE '~%'`, eventID, txnID)
}

func (s *Store) DeleteEvent(eventID string) {
	_, _ = s.db.Exec(`DELETE FROM events WHERE event_id=?`, eventID)
	_, _ = s.db.Exec(`DELETE FROM search WHERE event_id=?`, eventID)
}

// PurgeBefore borra los mensajes (no el estado) anteriores a ts: política de retención.
func (s *Store) PurgeBefore(roomID string, ts int64) int64 {
	_, _ = s.db.Exec(`DELETE FROM search WHERE room_id=? AND ts<?`, roomID, ts)
	res, err := s.db.Exec(`DELETE FROM events WHERE room_id=? AND ts<? AND state_key IS NULL AND status=''`, roomID, ts)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

func (s *Store) Redact(eventID, redactedBy string, keep json.RawMessage) {
	_, _ = s.db.Exec(`UPDATE events SET redacted_by=?, content=? WHERE event_id=?`, redactedBy, nz(keep, "{}"), eventID)
	_, _ = s.db.Exec(`DELETE FROM search WHERE event_id=?`, eventID)
}

// ClearTimeline elimina la línea de tiempo cacheada (tras un sync "limited"),
// conservando los ecos locales pendientes. El índice de búsqueda se conserva.
func (s *Store) ClearTimeline(roomID string) {
	_, _ = s.db.Exec(`DELETE FROM events WHERE room_id=? AND status=''`, roomID)
}

func (s *Store) MinSort(roomID string) int64 {
	var v sql.NullInt64
	_ = s.rd.QueryRow(`SELECT MIN(sort) FROM events WHERE room_id=?`, roomID).Scan(&v)
	if !v.Valid {
		return 0
	}
	return v.Int64
}

func (s *Store) MaxSortEvent(roomID string) (int64, string) {
	var sort int64
	var id string
	_ = s.rd.QueryRow(`SELECT sort, event_id FROM events WHERE room_id=? AND event_id NOT LIKE '~%' ORDER BY sort DESC LIMIT 1`,
		roomID).Scan(&sort, &id)
	return sort, id
}

func (s *Store) queryEvents(q string, args ...any) []*EventRow {
	rows, err := s.rd.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*EventRow
	for rows.Next() {
		if e, err := scanEvent(rows); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// RecentBySender devuelve los últimos mensajes (no estado, no borrados) de alguien en una sala.
func (s *Store) RecentBySender(roomID, sender string, limit int) []*EventRow {
	return s.queryEvents(`SELECT `+eventCols+` FROM events WHERE room_id=? AND sender=? AND state_key IS NULL
		AND redacted_by='' AND status='' AND type NOT IN ('m.room.redaction') ORDER BY sort DESC LIMIT ?`, roomID, sender, limit)
}

// RoomEventsRange devuelve todos los eventos visibles (incluidos los de hilos) entre dos fechas, en orden.
func (s *Store) RoomEventsRange(roomID string, from, to int64) []*EventRow {
	if to <= 0 {
		to = 1 << 62
	}
	return s.queryEvents(`SELECT `+eventCols+` FROM events WHERE room_id=? AND ts>=? AND ts<=? AND status='' AND
		rel_type NOT IN ('m.annotation','m.replace','m.reference') AND type NOT IN (`+hiddenTypes+`)
		AND type NOT LIKE 'm.key.verification.%' ORDER BY sort ASC`, roomID, from, to)
}

// TimelineBefore devuelve eventos "visibles" (no relaciones) de la sala con sort < before, en orden ascendente.
func (s *Store) TimelineBefore(roomID string, before int64, limit int) []*EventRow {
	if before <= 0 {
		before = 1 << 62
	}
	evts := s.queryEvents(`SELECT `+eventCols+` FROM events WHERE room_id=? AND sort<? AND thread_root='' AND
		rel_type NOT IN ('m.annotation','m.replace','m.reference') AND type NOT IN (`+hiddenTypes+`)
		AND type NOT LIKE 'm.key.verification.%' ORDER BY sort DESC LIMIT ?`, roomID, before, limit)
	for i, j := 0, len(evts)-1; i < j; i, j = i+1, j-1 {
		evts[i], evts[j] = evts[j], evts[i]
	}
	return evts
}

func (s *Store) ThreadEvents(root string, limit int) []*EventRow {
	evts := s.queryEvents(`SELECT `+eventCols+` FROM events WHERE thread_root=? AND
		rel_type NOT IN ('m.annotation','m.replace') ORDER BY ts DESC LIMIT ?`, root, limit)
	for i, j := 0, len(evts)-1; i < j; i, j = i+1, j-1 {
		evts[i], evts[j] = evts[j], evts[i]
	}
	return evts
}

func (s *Store) Relations(eventIDs []string) []*EventRow {
	if len(eventIDs) == 0 {
		return nil
	}
	ph := strings.Repeat("?,", len(eventIDs))
	args := make([]any, len(eventIDs))
	for i, id := range eventIDs {
		args[i] = id
	}
	return s.queryEvents(`SELECT `+eventCols+` FROM events WHERE rel_to IN (`+ph[:len(ph)-1]+`) AND redacted_by='' ORDER BY sort`, args...)
}

type threadInfo struct {
	Count      int
	LatestSort int64
	LatestID   string
}

func (s *Store) ThreadSummaries(roots []string) map[string]threadInfo {
	out := map[string]threadInfo{}
	if len(roots) == 0 {
		return out
	}
	ph := strings.Repeat("?,", len(roots))
	args := make([]any, len(roots))
	for i, id := range roots {
		args[i] = id
	}
	rows, err := s.rd.Query(`SELECT thread_root, COUNT(*), MAX(sort) FROM events WHERE thread_root IN (`+ph[:len(ph)-1]+`)
		AND rel_type NOT IN ('m.annotation','m.replace') AND redacted_by='' GROUP BY thread_root`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var root string
		var ti threadInfo
		if rows.Scan(&root, &ti.Count, &ti.LatestSort) == nil {
			out[root] = ti
		}
	}
	for root, ti := range out {
		_ = s.rd.QueryRow(`SELECT event_id FROM events WHERE sort=?`, ti.LatestSort).Scan(&ti.LatestID)
		out[root] = ti
	}
	return out
}

func (s *Store) UTDsForSession(sessionID string) []*EventRow {
	return s.queryEvents(`SELECT `+eventCols+` FROM events WHERE session_id=? AND decrypt_error!=''`, sessionID)
}

func (s *Store) AllUTDs(limit int) []*EventRow {
	return s.queryEvents(`SELECT `+eventCols+` FROM events WHERE decrypt_error!='' ORDER BY sort DESC LIMIT ?`, limit)
}

func (s *Store) PendingEvents() []*EventRow {
	return s.queryEvents(`SELECT ` + eventCols + ` FROM events WHERE status='sending'`)
}

// CountUnread cuenta mensajes de otros después del marcador de lectura.
func (s *Store) CountUnread(roomID, me string, readSort int64) int {
	var n int
	_ = s.rd.QueryRow(`SELECT COUNT(*) FROM events WHERE room_id=? AND sort>? AND sender!=? AND redacted_by='' AND
		thread_root='' AND rel_type NOT IN ('m.annotation','m.replace') AND
		type IN ('m.room.message','m.sticker','m.room.encrypted','org.matrix.msc3381.poll.start','m.poll.start','m.call.invite')`,
		roomID, readSort, me).Scan(&n)
	return n
}

func (s *Store) LastMessage(roomID string) *EventRow {
	e, err := scanEvent(s.rd.QueryRow(`SELECT `+eventCols+` FROM events WHERE room_id=? AND redacted_by='' AND
		thread_root='' AND rel_type NOT IN ('m.annotation','m.replace') AND
		type IN ('m.room.message','m.sticker','m.room.encrypted','org.matrix.msc3381.poll.start','m.poll.start','m.call.invite')
		ORDER BY sort DESC LIMIT 1`, roomID))
	if err != nil {
		return nil
	}
	return e
}

// --- búsqueda ---

func (s *Store) IndexText(roomID, eventID, sender string, ts int64, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	_, _ = s.db.Exec(`DELETE FROM search WHERE event_id=?`, eventID)
	_, _ = s.db.Exec(`INSERT INTO search(body, room_id, event_id, sender, ts) VALUES(?,?,?,?,?)`, body, roomID, eventID, sender, ts)
}

type SearchHit struct {
	RoomID  string `json:"room"`
	EventID string `json:"id"`
	Sender  string `json:"sender"`
	TS      int64  `json:"ts"`
	Snippet string `json:"snippet"`
}

func ftsQuery(q string) string {
	// Cada palabra se convierte en prefijo entre comillas: robusto ante caracteres especiales.
	var parts []string
	for _, w := range strings.Fields(q) {
		w = strings.ReplaceAll(w, `"`, `""`)
		parts = append(parts, `"`+w+`"*`)
	}
	return strings.Join(parts, " ")
}

func (s *Store) Search(query, roomID string, limit int) ([]SearchHit, error) {
	fq := ftsQuery(query)
	if fq == "" {
		return nil, nil
	}
	var rows *sql.Rows
	var err error
	if roomID != "" {
		rows, err = s.rd.Query(`SELECT room_id, event_id, sender, ts, snippet(search, 0, '[[', ']]', '…', 12) FROM search
			WHERE search MATCH ? AND room_id=? ORDER BY ts DESC LIMIT ?`, fq, roomID, limit)
	} else {
		rows, err = s.rd.Query(`SELECT room_id, event_id, sender, ts, snippet(search, 0, '[[', ']]', '…', 12) FROM search
			WHERE search MATCH ? ORDER BY ts DESC LIMIT ?`, fq, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		if rows.Scan(&h.RoomID, &h.EventID, &h.Sender, &h.TS, &h.Snippet) == nil {
			out = append(out, h)
		}
	}
	return out, nil
}

// --- account data ---

func (s *Store) PutAccountData(typ string, content json.RawMessage) {
	_, _ = s.db.Exec(`INSERT INTO account_data(type, content) VALUES(?,?) ON CONFLICT(type) DO UPDATE SET content=excluded.content`,
		typ, string(content))
}

func (s *Store) GetAccountData(typ string) json.RawMessage {
	var c string
	if s.rd.QueryRow(`SELECT content FROM account_data WHERE type=?`, typ).Scan(&c) != nil {
		return nil
	}
	return json.RawMessage(c)
}

func (s *Store) PutRoomAccountData(roomID, typ string, content json.RawMessage) {
	_, _ = s.db.Exec(`INSERT INTO room_account_data(room_id, type, content) VALUES(?,?,?)
		ON CONFLICT(room_id, type) DO UPDATE SET content=excluded.content`, roomID, typ, string(content))
}

func (s *Store) GetRoomAccountData(roomID, typ string) json.RawMessage {
	var c string
	if s.rd.QueryRow(`SELECT content FROM room_account_data WHERE room_id=? AND type=?`, roomID, typ).Scan(&c) != nil {
		return nil
	}
	return json.RawMessage(c)
}

// --- recibos de lectura ---

func (s *Store) PutReceipt(roomID, userID, eventID string, ts int64) {
	_, _ = s.db.Exec(`INSERT INTO receipts(room_id,user_id,event_id,ts) VALUES(?,?,?,?)
		ON CONFLICT(room_id,user_id) DO UPDATE SET event_id=excluded.event_id, ts=excluded.ts`, roomID, userID, eventID, ts)
}

func (s *Store) Receipts(roomID string) map[string][]string {
	rows, err := s.rd.Query(`SELECT user_id, event_id FROM receipts WHERE room_id=?`, roomID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var u, e string
		if rows.Scan(&u, &e) == nil {
			out[e] = append(out[e], u)
		}
	}
	return out
}

// --- borradores y programados ---

func (s *Store) SetDraft(roomID string, data json.RawMessage) {
	if len(data) == 0 || string(data) == "null" || string(data) == `""` {
		_, _ = s.db.Exec(`DELETE FROM drafts WHERE room_id=?`, roomID)
		return
	}
	_, _ = s.db.Exec(`INSERT INTO drafts(room_id,data) VALUES(?,?) ON CONFLICT(room_id) DO UPDATE SET data=excluded.data`,
		roomID, string(data))
}

func (s *Store) Drafts() map[string]json.RawMessage {
	rows, err := s.rd.Query(`SELECT room_id, data FROM drafts`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var r, d string
		if rows.Scan(&r, &d) == nil {
			out[r] = json.RawMessage(d)
		}
	}
	return out
}

type Scheduled struct {
	ID      int64           `json:"id"`
	RoomID  string          `json:"room"`
	SendAt  int64           `json:"send_at"`
	Payload json.RawMessage `json:"payload"`
}

func (s *Store) AddScheduled(roomID string, sendAt int64, payload json.RawMessage) int64 {
	res, err := s.db.Exec(`INSERT INTO scheduled(room_id, send_at, payload) VALUES(?,?,?)`, roomID, sendAt, string(payload))
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

func (s *Store) ListScheduled() []Scheduled {
	rows, err := s.rd.Query(`SELECT id, room_id, send_at, payload FROM scheduled ORDER BY send_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Scheduled
	for rows.Next() {
		var sc Scheduled
		var p string
		if rows.Scan(&sc.ID, &sc.RoomID, &sc.SendAt, &p) == nil {
			sc.Payload = json.RawMessage(p)
			out = append(out, sc)
		}
	}
	return out
}

func (s *Store) DeleteScheduled(id int64) {
	_, _ = s.db.Exec(`DELETE FROM scheduled WHERE id=?`, id)
}

var errNotFound = errors.New("no encontrado")

// WithTx ejecuta fn dentro de una transacción (para lotes grandes del sync).
func (s *Store) WithTx(ctx context.Context, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_, _ = s.db.Exec(`ROLLBACK`)
		return err
	}
	_, err := s.db.Exec(`COMMIT`)
	return err
}
