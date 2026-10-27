package core

import (
	"context"
	"encoding/json"
	"errors"
	"unsafe"
)

// VideoHost muestra el video nativo de las llamadas sobre la interfaz (lo implementa el shell).
// Attach recibe el sink de video de GStreamer (y se queda con su referencia).
type VideoHost interface {
	Attach(stream string, sink unsafe.Pointer)
	Place(stream string, x, y, w, h float64, visible, mirror, contain bool)
	Clear()
}

type VideoTile struct {
	Stream  string  `json:"stream"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	W       float64 `json:"w"`
	H       float64 `json:"h"`
	Visible bool    `json:"visible"`
	Mirror  bool    `json:"mirror"`
	Contain bool    `json:"contain"`
}

// attachStream entrega al shell el video de un flujo nuevo.
func (cm *CallManager) attachStream(c *Call, stream string) {
	host := cm.m.Video
	if host == nil {
		return
	}
	pe, ok := c.engine.(interface{ VideoSink(string) unsafe.Pointer })
	if !ok {
		return
	}
	p := pe.VideoSink(stream)
	cm.m.Log.Debug().Str("stream", stream).Bool("sink", p != nil).Msg("Video nativo")
	if p != nil {
		host.Attach(stream, p)
	}
}

type callP struct {
	ID string `json:"id"`
}

func init() {
	reg("call.place", acct(func(ctx context.Context, a *Account, p *struct {
		Room  string `json:"room"`
		Video bool   `json:"video"`
		Muted bool   `json:"muted"`
		Deaf  bool   `json:"deafened"`
	}) (any, error) {
		c, err := a.m.Calls.Place(a, p.Room, p.Video, p.Muted, p.Deaf)
		if err != nil {
			return nil, err
		}
		return c.View(), nil
	}))
	reg("call.answer", plain(func(ctx context.Context, m *Manager, p *struct {
		ID    string `json:"id"`
		Video bool   `json:"video"`
		Muted bool   `json:"muted"`
		Deaf  bool   `json:"deafened"`
	}) (any, error) {
		return ok(m.Calls.Answer(p.ID, p.Video, p.Muted, p.Deaf))
	}))
	reg("call.reject", plain(func(ctx context.Context, m *Manager, p *callP) (any, error) {
		return ok(m.Calls.Reject(p.ID))
	}))
	reg("call.hangup", plain(func(ctx context.Context, m *Manager, p *callP) (any, error) {
		return ok(m.Calls.Hangup(p.ID, ""))
	}))
	reg("call.mute", plain(func(ctx context.Context, m *Manager, p *struct {
		ID    string `json:"id"`
		Muted bool   `json:"muted"`
	}) (any, error) {
		m.Calls.SetMuted(p.ID, p.Muted)
		return ok(nil)
	}))
	reg("call.video", plain(func(ctx context.Context, m *Manager, p *struct {
		ID  string `json:"id"`
		Off bool   `json:"off"`
	}) (any, error) {
		m.Calls.SetVideoOff(p.ID, p.Off)
		return ok(nil)
	}))
	reg("call.deafen", plain(func(ctx context.Context, m *Manager, p *struct {
		ID   string `json:"id"`
		Deaf bool   `json:"deaf"`
	}) (any, error) {
		m.Calls.SetDeafened(p.ID, p.Deaf)
		return ok(nil)
	}))
	reg("call.screen", plain(func(ctx context.Context, m *Manager, p *struct {
		ID   string        `json:"id"`
		On   bool          `json:"on"`
		Opts ScreenOptions `json:"opts"`
	}) (any, error) {
		return ok(m.Calls.Screen(p.ID, p.On, p.Opts))
	}))
	reg("media.screens", plain(func(ctx context.Context, m *Manager, p *struct {
		Thumbs bool `json:"thumbs"`
	}) (any, error) {
		res := screenSources(ctx, p.Thumbs)
		res["last_window"] = uiString(m.Settings(), "shareRestoreWindow", "") != ""
		return res, nil
	}))
	reg("media.limits", acct(func(ctx context.Context, a *Account, _ *none) (any, error) {
		return map[string]any{"upload_size": a.uploadLimit(ctx)}, nil
	}))
	reg("media.audioApps", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		return audioApps(ctx)
	}))
	reg("call.list", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		return m.Calls.List(), nil
	}))
	reg("call.stats", plain(func(ctx context.Context, m *Manager, p *callP) (any, error) {
		c := m.Calls.get(p.ID)
		if c == nil || c.engine == nil {
			return nil, errors.New("no hay llamada")
		}
		return c.engine.Stats(), nil
	}))
	reg("call.layout", plain(func(ctx context.Context, m *Manager, p *struct {
		Tiles []VideoTile `json:"tiles"`
	}) (any, error) {
		m.Log.Debug().Interface("tiles", p.Tiles).Msg("Ubicación del video")
		if m.Video != nil {
			for _, t := range p.Tiles {
				m.Video.Place(t.Stream, t.X, t.Y, t.W, t.H, t.Visible, t.Mirror, t.Contain)
			}
		}
		return ok(nil)
	}))
	reg("call.caps", plain(func(ctx context.Context, m *Manager, p *struct {
		WebRTC bool `json:"webrtc"`
	}) (any, error) {
		m.Calls.SetBrowserWebRTC(p.WebRTC && m.Video == nil)
		return map[string]any{"native": m.Calls.NativeAvailable(), "native_video": m.Video != nil}, nil
	}))
	reg("app.snapshot", plain(func(ctx context.Context, m *Manager, _ *none) (any, error) {
		if m.Snapshot == nil {
			return nil, errors.New("no disponible")
		}
		m.Snapshot()
		return ok(nil)
	}))
	reg("call.engine.reply", plain(func(ctx context.Context, m *Manager, p *struct {
		Req    string          `json:"req"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}) (any, error) {
		m.Calls.EngineReply(p.Req, p.Result, p.Error)
		return ok(nil)
	}))
	reg("call.engine.event", plain(func(ctx context.Context, m *Manager, p *struct {
		Call string          `json:"call"`
		Kind string          `json:"kind"`
		Data json.RawMessage `json:"data"`
	}) (any, error) {
		m.Calls.EngineEvent(p.Call, p.Kind, p.Data)
		return ok(nil)
	}))
}
