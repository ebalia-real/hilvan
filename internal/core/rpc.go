package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"hilvan/internal/media"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// RPCHandler atiende un método llamado desde la interfaz.
type RPCHandler func(ctx context.Context, m *Manager, p json.RawMessage) (any, error)

var rpcMethods = map[string]RPCHandler{}

func reg(name string, h RPCHandler) { rpcMethods[name] = h }

// Call ejecuta un método RPC.
func (m *Manager) Call(ctx context.Context, method string, params json.RawMessage) (any, error) {
	h, ok := rpcMethods[method]
	if !ok {
		return nil, fmt.Errorf("método desconocido: %s", method)
	}
	return h(ctx, m, params)
}

func decode[T any](raw json.RawMessage) (*T, error) {
	p := new(T)
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, p); err != nil {
			return nil, fmt.Errorf("parámetros inválidos: %w", err)
		}
	}
	return p, nil
}

func plain[T any](fn func(context.Context, *Manager, *T) (any, error)) RPCHandler {
	return func(ctx context.Context, m *Manager, raw json.RawMessage) (any, error) {
		p, err := decode[T](raw)
		if err != nil {
			return nil, err
		}
		return fn(ctx, m, p)
	}
}

func acct[T any](fn func(context.Context, *Account, *T) (any, error)) RPCHandler {
	return func(ctx context.Context, m *Manager, raw json.RawMessage) (any, error) {
		var base struct {
			A string `json:"a"`
		}
		_ = json.Unmarshal(raw, &base)
		a, err := m.Account(base.A)
		if err != nil {
			return nil, err
		}
		p, err := decode[T](raw)
		if err != nil {
			return nil, err
		}
		return fn(ctx, a, p)
	}
}

type none struct{}

type roomP struct {
	Room string `json:"room"`
}

func ok(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func init() {
	// --- aplicación ---
	reg("app.info", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		return map[string]any{
			"version":      m.Version,
			"os":           runtime.GOOS,
			"accounts":     m.AccountsInfo(),
			"settings":     m.Settings(),
			"secrets":      m.secrets.Kind(),
			"native_calls": m.Calls.NativeAvailable(),
			"policy":       m.Policy(),
		}, nil
	}))
	reg("settings.get", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) { return m.Settings(), nil }))
	reg("settings.set", func(ctx context.Context, m *Manager, raw json.RawMessage) (any, error) { return m.UpdateSettings(raw) })
	reg("focus.set", plain(func(ctx context.Context, m *Manager, p *Focus) (any, error) {
		m.SetFocus(*p)
		if p.Window && p.Account != "" && p.Room != "" {
			m.Notifier.Close(p.Account, p.Room)
		}
		return ok(nil)
	}))
	reg("app.openExternal", plain(func(ctx context.Context, m *Manager, p *struct {
		URL string `json:"url"`
	}) (any, error) {
		if !strings.HasPrefix(p.URL, "https://") && !strings.HasPrefix(p.URL, "http://") && !strings.HasPrefix(p.URL, "mailto:") {
			return nil, errors.New("enlace no permitido")
		}
		if m.OpenURL == nil {
			return map[string]bool{"opened": false}, nil
		}
		m.OpenURL(p.URL)
		return map[string]bool{"opened": true}, nil
	}))
	reg("app.theme", plain(func(ctx context.Context, m *Manager, p *struct {
		Dark bool `json:"dark"`
	}) (any, error) {
		if m.SetDark != nil {
			if p.Dark {
				m.SetDark(1)
			} else {
				m.SetDark(0)
			}
		}
		return ok(nil)
	}))
	reg("app.navigate", plain(func(ctx context.Context, m *Manager, p *struct {
		Account string `json:"account"`
		Room    string `json:"room"`
		Event   string `json:"event"`
	}) (any, error) {
		m.Broadcast(Push{Ev: "open", A: p.Account, D: map[string]string{"room": p.Room, "event": p.Event}})
		return ok(nil)
	}))
	reg("accounts.list", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) { return m.AccountsInfo(), nil }))

	// --- autenticación ---
	reg("auth.discover", plain(func(ctx context.Context, m *Manager, p *struct {
		Server string `json:"server"`
	}) (any, error) {
		return m.Discover(ctx, p.Server)
	}))
	reg("auth.password", plain(func(ctx context.Context, m *Manager, p *struct {
		Homeserver string `json:"homeserver"`
		User       string `json:"user"`
		Password   string `json:"password"`
	}) (any, error) {
		return m.LoginPassword(ctx, p.Homeserver, p.User, p.Password)
	}))
	reg("auth.sso", plain(func(ctx context.Context, m *Manager, p *struct {
		Homeserver string `json:"homeserver"`
		IdP        string `json:"idp"`
	}) (any, error) {
		return m.StartSSO(ctx, p.Homeserver, p.IdP)
	}))
	reg("auth.oauth", plain(func(ctx context.Context, m *Manager, p *struct {
		Homeserver string `json:"homeserver"`
		Register   bool   `json:"register"`
	}) (any, error) {
		return m.StartOAuth(ctx, p.Homeserver, p.Register)
	}))
	reg("auth.register", plain(func(ctx context.Context, m *Manager, p *RegisterParams) (any, error) { return m.Register(ctx, *p) }))
	reg("account.logout", plain(func(ctx context.Context, m *Manager, p *struct {
		A string `json:"a"`
	}) (any, error) {
		return ok(m.Logout(ctx, p.A))
	}))
	reg("account.manageUrl", acct(func(ctx context.Context, a *Account, p *struct {
		Action string `json:"action"`
	}) (any, error) {
		return map[string]string{"url": a.accountManagementURL(p.Action)}, nil
	}))

	// --- salas y línea de tiempo ---
	reg("rooms.list", acct(func(ctx context.Context, a *Account, _ *none) (any, error) {
		return map[string]any{"rooms": a.RoomList(), "drafts": a.store.Drafts(), "scheduled": a.store.ListScheduled(),
			"ready": a.initialDone.Load(), "status": a.status.Load(), "account_data": a.accountDataForUI()}, nil
	}))
	reg("room.timeline", acct(func(ctx context.Context, a *Account, p *struct {
		Room   string `json:"room"`
		Before int64  `json:"before"`
		Limit  int    `json:"limit"`
	}) (any, error) {
		return a.Timeline(ctx, p.Room, p.Before, p.Limit)
	}))
	reg("room.event", acct(func(ctx context.Context, a *Account, p *struct {
		Room string `json:"room"`
		ID   string `json:"id"`
	}) (any, error) {
		it, profs, err := a.FetchEvent(ctx, p.Room, p.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"item": it, "profiles": profs}, nil
	}))
	reg("room.thread", acct(func(ctx context.Context, a *Account, p *struct {
		Room string `json:"room"`
		Root string `json:"root"`
	}) (any, error) {
		return a.Thread(ctx, p.Room, p.Root)
	}))
	reg("room.threads", acct(func(ctx context.Context, a *Account, p *roomP) (any, error) { return a.ThreadList(ctx, p.Room) }))
	reg("room.send", acct(func(ctx context.Context, a *Account, p *SendParams) (any, error) {
		txn, err := a.Send(ctx, p)
		if err != nil {
			return nil, err
		}
		return map[string]string{"txn": txn}, nil
	}))
	reg("room.react", acct(func(ctx context.Context, a *Account, p *struct {
		Room string `json:"room"`
		ID   string `json:"id"`
		Key  string `json:"key"`
	}) (any, error) {
		_, err := a.React(ctx, p.Room, p.ID, p.Key)
		return ok(err)
	}))
	reg("room.redact", acct(func(ctx context.Context, a *Account, p *struct {
		Room   string `json:"room"`
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}) (any, error) {
		return ok(a.Redact(ctx, p.Room, p.ID, p.Reason))
	}))
	reg("room.retry", acct(func(ctx context.Context, a *Account, p *struct {
		ID string `json:"id"`
	}) (any, error) {
		return ok(a.Retry(p.ID))
	}))
	reg("room.discard", acct(func(ctx context.Context, a *Account, p *struct {
		ID string `json:"id"`
	}) (any, error) {
		a.DiscardLocal(p.ID)
		return ok(nil)
	}))
	reg("room.read", acct(func(ctx context.Context, a *Account, p *struct {
		Room string `json:"room"`
		ID   string `json:"id"`
	}) (any, error) {
		return ok(a.MarkRead(ctx, p.Room, p.ID))
	}))
	reg("room.unread", acct(func(ctx context.Context, a *Account, p *struct {
		Room   string `json:"room"`
		Unread bool   `json:"unread"`
	}) (any, error) {
		return ok(a.MarkUnread(ctx, p.Room, p.Unread))
	}))
	reg("room.typing", acct(func(ctx context.Context, a *Account, p *struct {
		Room   string `json:"room"`
		Typing bool   `json:"typing"`
	}) (any, error) {
		return ok(a.Typing(ctx, p.Room, p.Typing))
	}))
	reg("room.members", acct(func(ctx context.Context, a *Account, p *roomP) (any, error) { return a.Members(ctx, p.Room) }))
	reg("room.state", acct(func(ctx context.Context, a *Account, p *struct {
		Room  string   `json:"room"`
		Types []string `json:"types"`
	}) (any, error) {
		return a.RoomState(p.Room, p.Types), nil
	}))
	reg("room.setState", acct(func(ctx context.Context, a *Account, p *struct {
		Room     string          `json:"room"`
		Type     string          `json:"type"`
		StateKey string          `json:"state_key"`
		Content  json.RawMessage `json:"content"`
	}) (any, error) {
		return ok(a.SetState(ctx, p.Room, p.Type, p.StateKey, p.Content))
	}))
	reg("room.power", acct(func(ctx context.Context, a *Account, p *struct {
		Room  string `json:"room"`
		User  string `json:"user"`
		Level int    `json:"level"`
	}) (any, error) {
		return ok(a.SetPowerLevel(ctx, p.Room, p.User, p.Level))
	}))
	reg("room.create", acct(func(ctx context.Context, a *Account, p *CreateRoomParams) (any, error) {
		r, err := a.CreateRoom(ctx, *p)
		if err != nil {
			return nil, err
		}
		return map[string]string{"room": r}, nil
	}))
	reg("room.join", acct(func(ctx context.Context, a *Account, p *struct {
		Target string   `json:"target"`
		Via    []string `json:"via"`
		Reason string   `json:"reason"`
	}) (any, error) {
		r, err := a.Join(ctx, p.Target, p.Via, p.Reason)
		if err != nil {
			return nil, err
		}
		return map[string]string{"room": r}, nil
	}))
	reg("room.knock", acct(func(ctx context.Context, a *Account, p *struct {
		Target string   `json:"target"`
		Via    []string `json:"via"`
		Reason string   `json:"reason"`
	}) (any, error) {
		return ok(a.Knock(ctx, p.Target, p.Via, p.Reason))
	}))
	reg("room.leave", acct(func(ctx context.Context, a *Account, p *struct {
		Room   string `json:"room"`
		Forget bool   `json:"forget"`
	}) (any, error) {
		return ok(a.Leave(ctx, p.Room, p.Forget))
	}))
	type memberAct struct {
		Room   string `json:"room"`
		User   string `json:"user"`
		Reason string `json:"reason"`
		Ban    bool   `json:"ban"`
	}
	reg("room.invite", acct(func(ctx context.Context, a *Account, p *memberAct) (any, error) {
		return ok(a.Invite(ctx, p.Room, p.User, p.Reason))
	}))
	reg("room.kick", acct(func(ctx context.Context, a *Account, p *memberAct) (any, error) {
		return ok(a.Kick(ctx, p.Room, p.User, p.Reason))
	}))
	reg("room.ban", acct(func(ctx context.Context, a *Account, p *memberAct) (any, error) {
		return ok(a.Ban(ctx, p.Room, p.User, p.Reason, p.Ban))
	}))
	reg("room.tag", acct(func(ctx context.Context, a *Account, p *struct {
		Room string `json:"room"`
		Tag  string `json:"tag"`
		On   bool   `json:"on"`
	}) (any, error) {
		return ok(a.SetTag(ctx, p.Room, p.Tag, p.On))
	}))
	reg("room.notify", acct(func(ctx context.Context, a *Account, p *struct {
		Room string `json:"room"`
		Mode string `json:"mode"`
	}) (any, error) {
		return ok(a.SetNotifyMode(ctx, p.Room, p.Mode))
	}))
	reg("room.pinned", acct(func(ctx context.Context, a *Account, p *roomP) (any, error) {
		items, profs := a.Pinned(ctx, p.Room)
		return map[string]any{"items": items, "profiles": profs}, nil
	}))
	reg("room.preview", acct(func(ctx context.Context, a *Account, p *struct {
		Target string   `json:"target"`
		Via    []string `json:"via"`
	}) (any, error) {
		return a.RoomPreview(ctx, p.Target, p.Via)
	}))
	reg("room.draft", acct(func(ctx context.Context, a *Account, p *struct {
		Room string          `json:"room"`
		Data json.RawMessage `json:"data"`
	}) (any, error) {
		a.store.SetDraft(p.Room, p.Data)
		return ok(nil)
	}))
	reg("room.report", acct(func(ctx context.Context, a *Account, p *struct {
		Room   string `json:"room"`
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}) (any, error) {
		return ok(friendlyErrorNil(a.cli.ReportEvent(ctx, id.RoomID(p.Room), id.EventID(p.ID), p.Reason)))
	}))
	reg("file.save", acct(func(ctx context.Context, a *Account, p *struct {
		Room  string `json:"room"`
		Event string `json:"event"`
	}) (any, error) {
		// Puede tardar mucho con archivos enormes: no depende de que la interfaz espere.
		go func() {
			path, err := a.SaveFile(a.ctx, p.Room, p.Event, "")
			d := map[string]any{"room": p.Room, "event": p.Event, "path": path, "ok": err == nil}
			if err != nil {
				d["error"] = err.Error()
			}
			a.m.Broadcast(Push{Ev: "file.saved", A: a.UserID(), D: d})
		}()
		return ok(nil)
	}))
	// Guardar como: el diálogo se abre ya; la descarga sigue sola y avisa con "file.saved".
	reg("file.saveAs", acct(func(ctx context.Context, a *Account, p *struct {
		Room  string `json:"room"`
		Event string `json:"event"`
	}) (any, error) {
		dest, save, err := a.SaveAs(ctx, p.Room, p.Event)
		if errors.Is(err, ErrCanceled) {
			return map[string]any{"canceled": true}, nil
		}
		if err != nil {
			return nil, err
		}
		go func() {
			err := save()
			d := map[string]any{"room": p.Room, "event": p.Event, "path": dest, "ok": err == nil}
			if err != nil {
				d["error"] = err.Error()
			}
			a.m.Broadcast(Push{Ev: "file.saved", A: a.UserID(), D: d})
		}()
		return map[string]any{"path": dest}, nil
	}))
	reg("room.export", acct(func(ctx context.Context, a *Account, p *ExportParams) (any, error) {
		// Puede tardar (historia larga, adjuntos): se avisa por el evento "export".
		go func() {
			if _, err := a.ExportRoom(a.ctx, *p); err != nil {
				a.pushExport(p.Room, map[string]any{"state": "error", "error": err.Error()})
			}
		}()
		return ok(nil)
	}))
	reg("app.report", plain(func(ctx context.Context, m *Manager, p *ReportParams) (any, error) {
		return m.Report(ctx, *p)
	}))
	reg("app.sbom", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		path := uniquePath(filepath.Join(downloadsDir(), "Hilvan-"+m.Version+"-sbom.cdx.json"))
		if err := os.WriteFile(path, SBOM(m.Version), 0o644); err != nil {
			return nil, err
		}
		return map[string]string{"path": path}, nil
	}))
	reg("admin.advisories", acct(func(ctx context.Context, a *Account, _ *none) (any, error) {
		return a.SecurityAdvisories(ctx)
	}))
	reg("room.redactRecent", acct(func(ctx context.Context, a *Account, p *struct {
		Room   string `json:"room"`
		User   string `json:"user"`
		Limit  int    `json:"limit"`
		Reason string `json:"reason"`
	}) (any, error) {
		n, err := a.RedactRecent(a.ctx, p.Room, p.User, p.Limit, p.Reason)
		return map[string]int{"redacted": n}, err
	}))
	reg("policy.get", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) { return m.Policy(), nil }))
	reg("account.pinning", acct(func(ctx context.Context, a *Account, p *struct {
		Enable *bool `json:"enable"`
	}) (any, error) {
		if p.Enable != nil {
			a.SetPinning(*p.Enable)
		}
		return a.PinInfo(), nil
	}))
	reg("dm.open", acct(func(ctx context.Context, a *Account, p *struct {
		User string `json:"user"`
	}) (any, error) {
		r, err := a.OpenDM(ctx, p.User)
		if err != nil {
			return nil, err
		}
		return map[string]string{"room": r}, nil
	}))

	// --- encuestas y programados ---
	reg("poll.create", acct(func(ctx context.Context, a *Account, p *struct {
		Room      string   `json:"room"`
		Question  string   `json:"question"`
		Answers   []string `json:"answers"`
		Max       int      `json:"max"`
		Disclosed bool     `json:"disclosed"`
	}) (any, error) {
		_, err := a.SendPoll(p.Room, p.Question, p.Answers, p.Max, p.Disclosed)
		return ok(err)
	}))
	reg("poll.vote", acct(func(ctx context.Context, a *Account, p *struct {
		Room    string   `json:"room"`
		Poll    string   `json:"poll"`
		Answers []string `json:"answers"`
	}) (any, error) {
		_, err := a.VotePoll(p.Room, p.Poll, p.Answers)
		return ok(err)
	}))
	reg("poll.end", acct(func(ctx context.Context, a *Account, p *struct {
		Room string `json:"room"`
		Poll string `json:"poll"`
	}) (any, error) {
		_, err := a.EndPoll(p.Room, p.Poll)
		return ok(err)
	}))
	reg("schedule.add", acct(func(ctx context.Context, a *Account, p *struct {
		Params SendParams `json:"params"`
		At     int64      `json:"at"`
	}) (any, error) {
		sid, err := a.Schedule(&p.Params, p.At)
		if err != nil {
			return nil, err
		}
		return map[string]int64{"id": sid}, nil
	}))
	reg("schedule.list", acct(func(ctx context.Context, a *Account, _ *none) (any, error) { return a.store.ListScheduled(), nil }))
	reg("schedule.delete", acct(func(ctx context.Context, a *Account, p *struct {
		ID int64 `json:"id"`
	}) (any, error) {
		a.store.DeleteScheduled(p.ID)
		a.m.Broadcast(Push{Ev: "scheduled", A: a.UserID(), D: a.store.ListScheduled()})
		return ok(nil)
	}))

	// --- búsqueda y usuarios ---
	reg("search.local", acct(func(ctx context.Context, a *Account, p *struct {
		Query string `json:"query"`
		Room  string `json:"room"`
	}) (any, error) {
		// En una sala, primero se baja el historial que falte: si no, lo viejo no aparece.
		complete := true
		if p.Room != "" {
			complete = a.fillHistory(ctx, p.Room, 25*time.Second)
		}
		hits, err := a.store.Search(p.Query, p.Room, 200)
		return map[string]any{"hits": hits, "complete": complete, "pending_rooms": a.historyPending()}, err
	}))
	reg("search.server", acct(func(ctx context.Context, a *Account, p *struct {
		Query string `json:"query"`
		Room  string `json:"room"`
	}) (any, error) {
		hits, err := a.ServerSearch(ctx, p.Query, p.Room)
		return map[string]any{"hits": hits}, err
	}))
	reg("user.profile", acct(func(ctx context.Context, a *Account, p *struct {
		User string `json:"user"`
	}) (any, error) {
		return a.UserProfile(ctx, p.User)
	}))
	reg("user.search", acct(func(ctx context.Context, a *Account, p *struct {
		Query string `json:"query"`
	}) (any, error) {
		return a.SearchUsers(ctx, p.Query)
	}))
	reg("user.ignore", acct(func(ctx context.Context, a *Account, p *struct {
		User   string `json:"user"`
		Ignore bool   `json:"ignore"`
	}) (any, error) {
		return ok(a.Ignore(ctx, p.User, p.Ignore))
	}))
	reg("user.devices", acct(func(ctx context.Context, a *Account, p *struct {
		User string `json:"user"`
	}) (any, error) {
		return a.UserDevices(ctx, p.User)
	}))
	reg("profile.set", acct(func(ctx context.Context, a *Account, p *struct {
		Name   *string `json:"name"`
		Avatar *string `json:"avatar"`
	}) (any, error) {
		return ok(a.SetProfile(ctx, p.Name, p.Avatar))
	}))
	reg("status.set", acct(func(ctx context.Context, a *Account, p *UserStatus) (any, error) {
		return ok(a.SetStatus(ctx, *p))
	}))
	reg("presence.set", acct(func(ctx context.Context, a *Account, p *struct {
		Presence string `json:"presence"`
		Status   string `json:"status"`
	}) (any, error) {
		return ok(a.SetPresence(ctx, p.Presence, p.Status))
	}))

	// --- espacios y directorio ---
	reg("space.hierarchy", acct(func(ctx context.Context, a *Account, p *struct {
		Room string `json:"room"`
		From string `json:"from"`
	}) (any, error) {
		return a.Hierarchy(ctx, p.Room, p.From)
	}))
	reg("space.child", acct(func(ctx context.Context, a *Account, p *struct {
		Space     string `json:"space"`
		Child     string `json:"child"`
		Add       bool   `json:"add"`
		Suggested bool   `json:"suggested"`
	}) (any, error) {
		return ok(a.SpaceChild(ctx, p.Space, p.Child, p.Add, p.Suggested))
	}))
	reg("directory.public", acct(func(ctx context.Context, a *Account, p *struct {
		Server string `json:"server"`
		Query  string `json:"query"`
		Since  string `json:"since"`
	}) (any, error) {
		return a.PublicRooms(ctx, p.Server, p.Query, p.Since)
	}))

	// --- cifrado ---
	reg("crypto.status", acct(func(ctx context.Context, a *Account, _ *none) (any, error) { return a.CryptoStatus(ctx), nil }))
	reg("crypto.setup", acct(func(ctx context.Context, a *Account, p *struct {
		Password   string `json:"password"`
		Passphrase string `json:"passphrase"`
	}) (any, error) {
		key, err := a.SetupRecovery(ctx, p.Password, p.Passphrase)
		var appr *ApprovalRequired
		if errors.As(err, &appr) {
			return map[string]string{"approval_url": appr.URL}, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]string{"recovery_key": key}, nil
	}))
	reg("crypto.restore", acct(func(ctx context.Context, a *Account, p *struct {
		Key string `json:"key"`
	}) (any, error) {
		return a.RestoreWithRecoveryKey(ctx, p.Key)
	}))
	reg("crypto.export", acct(func(ctx context.Context, a *Account, p *struct {
		Passphrase string `json:"passphrase"`
	}) (any, error) {
		data, err := a.ExportKeys(ctx, p.Passphrase)
		if err != nil {
			return nil, err
		}
		return map[string]string{"data": string(data)}, nil
	}))
	reg("crypto.import", acct(func(ctx context.Context, a *Account, p *struct {
		Passphrase string `json:"passphrase"`
		Data       string `json:"data"`
		Base64     bool   `json:"base64"`
	}) (any, error) {
		data := []byte(p.Data)
		if p.Base64 {
			var err error
			if data, err = base64.StdEncoding.DecodeString(p.Data); err != nil {
				return nil, err
			}
		}
		n, total, err := a.ImportKeys(ctx, p.Passphrase, data)
		if err != nil {
			return nil, errors.New("no se pudo importar: frase incorrecta o archivo dañado")
		}
		return map[string]int{"imported": n, "total": total}, nil
	}))
	reg("verify.start", acct(func(ctx context.Context, a *Account, p *struct {
		User string `json:"user"`
		Room string `json:"room"`
	}) (any, error) {
		txn, err := a.StartVerification(ctx, p.User, p.Room)
		if err != nil {
			return nil, err
		}
		return map[string]string{"txn": txn}, nil
	}))
	reg("verify.action", acct(func(ctx context.Context, a *Account, p *struct {
		Txn    string `json:"txn"`
		Action string `json:"action"`
	}) (any, error) {
		return ok(a.VerifyAction(ctx, p.Txn, p.Action))
	}))
	reg("identity.accept", acct(func(ctx context.Context, a *Account, p *struct {
		User string `json:"user"`
	}) (any, error) {
		return ok(a.AcceptIdentity(ctx, p.User))
	}))
	reg("devices.list", acct(func(ctx context.Context, a *Account, _ *none) (any, error) { return a.OwnDevices(ctx) }))
	reg("devices.delete", acct(func(ctx context.Context, a *Account, p *struct {
		IDs      []string `json:"ids"`
		Password string   `json:"password"`
	}) (any, error) {
		err := a.DeleteDevices(ctx, p.IDs, p.Password)
		var appr *ApprovalRequired
		if errors.As(err, &appr) {
			return map[string]string{"approval_url": appr.URL}, nil
		}
		return ok(err)
	}))
	reg("devices.rename", acct(func(ctx context.Context, a *Account, p *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}) (any, error) {
		return ok(friendlyErrorNil(a.cli.SetDeviceInfo(ctx, id.DeviceID(p.ID), &mautrix.ReqDeviceInfo{DisplayName: p.Name})))
	}))

	// --- preferencias de cuenta ---
	reg("notify.rules", acct(func(ctx context.Context, a *Account, _ *none) (any, error) { return a.NotifyRules(ctx) }))
	reg("notify.category", acct(func(ctx context.Context, a *Account, p *struct {
		Key string `json:"key"`
		On  bool   `json:"on"`
	}) (any, error) {
		return ok(a.SetNotifyCategory(ctx, p.Key, p.On))
	}))
	reg("notify.keyword", acct(func(ctx context.Context, a *Account, p *struct {
		Keyword string `json:"keyword"`
		Remove  bool   `json:"remove"`
	}) (any, error) {
		if p.Remove {
			return ok(a.RemoveKeyword(ctx, p.Keyword))
		}
		return ok(a.AddKeyword(ctx, p.Keyword))
	}))
	approval := func(err error) (any, error) {
		var appr *ApprovalRequired
		if errors.As(err, &appr) {
			return map[string]string{"approval_url": appr.URL}, nil
		}
		return ok(err)
	}
	reg("account.password", acct(func(ctx context.Context, a *Account, p *struct {
		Old          string `json:"old"`
		New          string `json:"new"`
		LogoutOthers bool   `json:"logout_others"`
	}) (any, error) {
		return approval(a.ChangePassword(ctx, p.Old, p.New, p.LogoutOthers))
	}))
	reg("account.deactivate", acct(func(ctx context.Context, a *Account, p *struct {
		Password string `json:"password"`
		Erase    bool   `json:"erase"`
	}) (any, error) {
		return approval(a.Deactivate(ctx, p.Password, p.Erase))
	}))
	reg("account.signOutOthers", acct(func(ctx context.Context, a *Account, p *struct {
		Password string `json:"password"`
	}) (any, error) {
		return approval(a.SignOutOthers(ctx, p.Password))
	}))
	reg("account.3pids", acct(func(ctx context.Context, a *Account, _ *none) (any, error) { return a.ThreePIDs(ctx) }))
	reg("account.3pidDelete", acct(func(ctx context.Context, a *Account, p *struct {
		Medium  string `json:"medium"`
		Address string `json:"address"`
	}) (any, error) {
		return ok(a.DeleteThreePID(ctx, p.Medium, p.Address))
	}))
	reg("account.emailRequest", acct(func(ctx context.Context, a *Account, p *struct {
		Email string `json:"email"`
	}) (any, error) {
		sid, err := a.RequestEmailToken(ctx, p.Email)
		if err != nil {
			return nil, err
		}
		return map[string]string{"sid": sid}, nil
	}))
	reg("account.emailAdd", acct(func(ctx context.Context, a *Account, p *struct {
		SID      string `json:"sid"`
		Password string `json:"password"`
	}) (any, error) {
		return ok(a.AddThreePID(ctx, p.SID, p.Password))
	}))
	reg("account.resync", acct(func(ctx context.Context, a *Account, _ *none) (any, error) { return ok(a.Resync(ctx)) }))
	reg("user.ignored", acct(func(ctx context.Context, a *Account, _ *none) (any, error) { return a.IgnoredUsers(), nil }))
	reg("url.preview", acct(func(ctx context.Context, a *Account, p *struct {
		URL string `json:"url"`
	}) (any, error) {
		return a.URLPreview(ctx, p.URL)
	}))
	reg("media.devices", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		devs, err := media.Devices()
		if devs == nil {
			devs = []media.Device{}
		}
		return map[string]any{"native": media.Available(), "devices": devs}, err
	}))
	reg("media.mictest", plain(func(ctx context.Context, m *Manager, p *struct {
		Stop bool `json:"stop"`
		media.MicTestOptions
	}) (any, error) {
		if p.Stop {
			media.StopMicTest()
			return ok(nil)
		}
		var last time.Time
		err := media.StartMicTest(p.MicTestOptions, func(rms, peak float64) {
			if time.Since(last) < 45*time.Millisecond {
				return
			}
			last = time.Now()
			m.Broadcast(Push{Ev: "mictest", D: map[string]float64{"rms": rms, "peak": peak}})
		})
		return ok(err)
	}))
	reg("cache.clear", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		m.CleanMediaCache(0)
		return ok(nil)
	}))
	// Ícono de la app (estilos como en Telegram/Discord) y bandeja del sistema.
	reg("app.icon.set", plain(func(ctx context.Context, m *Manager, p *struct {
		Style string          `json:"style"`
		Cfg   json.RawMessage `json:"cfg"`
		PNG   []byte          `json:"png"` // base64
	}) (any, error) {
		if err := m.SetAppIcon(p.Style, p.Cfg, p.PNG); err != nil {
			return nil, err
		}
		return ok(nil)
	}))
	reg("file.saveText", plain(func(ctx context.Context, m *Manager, p *struct {
		Name string `json:"name"`
		Text string `json:"text"`
	}) (any, error) {
		path, err := m.SaveText(p.Name, p.Text)
		if errors.Is(err, ErrCanceled) {
			return map[string]any{"canceled": true}, nil
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": path}, nil
	}))
	reg("update.status", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		st := m.UpdateStatus()
		return map[string]any{"info": st, "packaging": Packaging}, nil
	}))
	reg("update.check", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		return m.CheckUpdate(ctx)
	}))
	reg("update.install", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		return ok(m.InstallUpdate())
	}))
	reg("app.platform", plain(func(ctx context.Context, m *Manager, p *struct {
		Action string `json:"action"`
	}) (any, error) {
		if m.Platform == nil {
			return "", nil
		}
		return m.Platform(p.Action), nil
	}))
	reg("app.tray", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		avail := m.TrayAvailable != nil && m.TrayAvailable()
		return map[string]any{"shell": m.TrayAvailable != nil, "available": avail, "launcher": runtime.GOOS == "linux" && !m.Paths.Portable}, nil
	}))
	reg("app.jank", plain(func(ctx context.Context, m *Manager, p *struct {
		MS     int      `json:"ms"`
		Events []string `json:"events"`
		View   string   `json:"view"`
	}) (any, error) {
		m.Log.Warn().Int("ms", p.MS).Strs("eventos", p.Events).Msg("Tirón en la interfaz")
		return ok(nil)
	}))
	reg("app.quit", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		if m.Quit != nil {
			go func() { time.Sleep(200 * time.Millisecond); m.Quit() }()
		}
		return ok(nil)
	}))

	// --- emojis y stickers ---
	reg("emotes.list", acct(func(ctx context.Context, a *Account, p *roomP) (any, error) { return a.EmotePacks(p.Room), nil }))
	reg("emotes.save", acct(func(ctx context.Context, a *Account, p *struct {
		Room     string          `json:"room"`
		StateKey string          `json:"state_key"`
		Content  json.RawMessage `json:"content"`
	}) (any, error) {
		return ok(a.SaveEmotePack(ctx, p.Room, p.StateKey, p.Content))
	}))
	reg("emotes.global", acct(func(ctx context.Context, a *Account, p *struct {
		Room     string `json:"room"`
		StateKey string `json:"state_key"`
		On       bool   `json:"on"`
	}) (any, error) {
		return ok(a.SetPackGlobal(ctx, p.Room, p.StateKey, p.On))
	}))

	// --- administración (Synapse) ---
	reg("admin.check", acct(func(ctx context.Context, a *Account, _ *none) (any, error) {
		return map[string]bool{"admin": a.IsServerAdmin(ctx)}, nil
	}))
	reg("admin.request", acct(func(ctx context.Context, a *Account, p *struct {
		Method string            `json:"method"`
		Path   string            `json:"path"`
		Query  map[string]string `json:"query"`
		Body   json.RawMessage   `json:"body"`
	}) (any, error) {
		return a.AdminRequest(ctx, p.Method, p.Path, p.Query, p.Body)
	}))
}

func (a *Account) accountDataForUI() map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, t := range []string{"m.direct", "im.ponies.user_emotes", "im.ponies.emote_rooms", "io.hilvan.bookmarks", "org.matrix.msc3230.space_order"} {
		if c := a.store.GetAccountData(t); c != nil {
			out[t] = c
		}
	}
	return out
}
