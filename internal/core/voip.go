package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"maunium.net/go/mautrix/event"
)

// Llamadas 1:1 con Matrix VoIP v1 (m.call.*). La señalización vive acá; el audio/video
// lo maneja un motor intercambiable: nativo (GStreamer) o el WebRTC de la interfaz.

const callLifetime = 60 * time.Second

// sdpMetadataChanged (m.call.sdp_stream_metadata_changed) no viene registrado en mautrix.
var sdpMetadataChanged = event.Type{Type: "m.call.sdp_stream_metadata_changed", Class: event.MessageEventType}

type sdpMetadataContent struct {
	CallID   string          `json:"call_id"`
	PartyID  string          `json:"party_id"`
	Version  json.RawMessage `json:"version"`
	Metadata json.RawMessage `json:"sdp_stream_metadata"`
}

func init() {
	event.TypeMap[sdpMetadataChanged] = reflect.TypeOf(sdpMetadataContent{})
}

type CallCandidate struct {
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdpMid,omitempty"`
	SDPMLineIndex *int   `json:"sdpMLineIndex,omitempty"`
}

// CallEngine es lo que necesita la señalización de un motor de medios.
type CallEngine interface {
	Offer(video bool) (string, error)
	Answer(offer string, video bool) (string, error)
	SetAnswer(sdp string) error
	Renegotiate(offer string) (string, error)
	AddCandidates(c []CallCandidate)
	SetMicMuted(bool)
	SetVideoMuted(bool)
	SetVolumes(mic, spk float64)
	SetRemoteVolume(v float64)
	SetDevice(kind, id string) error
	StartScreen(o ScreenOptions) error
	ScreenQuality(o ScreenOptions)
	StopScreen()
	Stats() any
	Close()
}

// ScreenOptions es lo que se elige al transmitir (como "Transmitir" en Discord).
type ScreenOptions struct {
	Source      string  `json:"source"` // monitor:<conector> | window | window:last | screen (selector del sistema)
	Res         string  `json:"res"`    // 720 | 1080 | 1440 | 2160 | source
	FPS         int     `json:"fps"`    // 5 | 15 | 30 | 60
	Mbps        float64 `json:"mbps"`   // bitrate máximo
	Audio       string  `json:"audio"`  // "" sin audio | "system" | número de serie de la app en PipeWire
	AudioVolume float64 `json:"audio_volume"`
	Hint        string  `json:"hint"` // auto | motion | detail
}

func (o ScreenOptions) size() (w, h int) {
	switch o.Res {
	case "720":
		return 1280, 720
	case "1080":
		return 1920, 1080
	case "1440":
		return 2560, 1440
	case "2160":
		return 3840, 2160
	}
	return 0, 0
}

func (o *ScreenOptions) normalize() {
	if o.FPS <= 0 || o.FPS > 60 {
		o.FPS = 30
	}
	if o.Mbps <= 0 {
		o.Mbps = 8
	}
	if o.Mbps > 50 {
		o.Mbps = 50
	}
	if o.AudioVolume <= 0 {
		o.AudioVolume = 1
	}
	if o.Res == "" {
		o.Res = "1080"
	}
}

type Call struct {
	cm         *CallManager
	a          *Account
	key        string // cuenta|call_id: con varias cuentas abiertas, cada una tiene su propia llamada
	ID         string
	Account    string
	Room       string
	Peer       string
	PeerName   string
	PeerAvatar string
	Outgoing   bool
	Video      bool
	State      string // ringing | inviting | connecting | connected | ended
	Reason     string
	Muted      bool
	VideoOff   bool
	Sharing    bool
	Share      *ScreenOptions
	Deafened   bool
	Engine     string // native | browser
	StartedAt  int64
	ConnectAt  int64
	RemoteVid  bool
	Streams    []string
	Speaking   map[string]bool

	partyID     string
	peerParty   string
	offer       string
	inviteVideo bool
	engine      CallEngine
	mu          sync.Mutex
	remoteSet   bool
	pendingIn   []CallCandidate
	pendingOut  []CallCandidate
	outTimer    *time.Timer
	inviteSent  bool
	timeout     *time.Timer
	discTimer   *time.Timer
	ended       bool
}

type CallManager struct {
	m  *Manager
	mu sync.Mutex
	// calls por call_id
	calls map[string]*Call
	// NativeFactory crea motores nativos (lo provee main en Linux). nil = solo navegador.
	NativeFactory func(c *Call, cfg NativeEngineConfig) (CallEngine, error)
	// browser: la interfaz anunció que puede hacer WebRTC
	browserWebRTC bool
	jsWaiters     map[string]chan jsReply
	jsSeq         int
	// OnStream / OnEnded los usa el shell para mostrar u ocultar el video nativo.
	OnStream func(c *Call, stream string)
	OnEnded  func(c *Call)
}

type NativeEngineConfig struct {
	TURN       []string
	STUN       string
	ForceRelay bool
	Settings   Settings
}

func newCallManager(m *Manager) *CallManager {
	return &CallManager{m: m, calls: map[string]*Call{}, jsWaiters: map[string]chan jsReply{}}
}

// NativeAvailable indica si hay motor de medios nativo (GStreamer).
func (cm *CallManager) NativeAvailable() bool { return cm.NativeFactory != nil }

// CallView es la foto de una llamada que recibe la interfaz.
type CallView struct {
	ID         string          `json:"id"`      // clave interna (cuenta|call_id)
	CallID     string          `json:"call_id"` // call_id de Matrix
	Account    string          `json:"account"`
	Room       string          `json:"room"`
	Peer       string          `json:"peer"`
	PeerName   string          `json:"peer_name"`
	PeerAvatar string          `json:"peer_avatar,omitempty"`
	Outgoing   bool            `json:"outgoing"`
	Video      bool            `json:"video"`
	State      string          `json:"state"`
	Reason     string          `json:"reason,omitempty"`
	Muted      bool            `json:"muted"`
	VideoOff   bool            `json:"video_off"`
	Sharing    bool            `json:"sharing"`
	Share      *ScreenOptions  `json:"share,omitempty"`
	Deafened   bool            `json:"deafened"`
	Engine     string          `json:"engine"`
	StartedAt  int64           `json:"started_at"`
	ConnectAt  int64           `json:"connected_at,omitempty"`
	RemoteVid  bool            `json:"remote_video"`
	Streams    []string        `json:"streams"`
	Speaking   map[string]bool `json:"speaking"`
}

func (c *Call) View() *CallView {
	c.mu.Lock()
	defer c.mu.Unlock()
	sp := make(map[string]bool, len(c.Speaking))
	for k, v := range c.Speaking {
		sp[k] = v
	}
	return &CallView{ID: c.key, CallID: c.ID, Account: c.Account, Room: c.Room, Peer: c.Peer, PeerName: c.PeerName, PeerAvatar: c.PeerAvatar,
		Outgoing: c.Outgoing, Video: c.Video, State: c.State, Reason: c.Reason, Muted: c.Muted, VideoOff: c.VideoOff,
		Sharing: c.Sharing, Share: c.Share, Deafened: c.Deafened, Engine: c.Engine, StartedAt: c.StartedAt, ConnectAt: c.ConnectAt,
		RemoteVid: c.RemoteVid, Streams: append([]string(nil), c.Streams...), Speaking: sp}
}

func (cm *CallManager) push(c *Call) {
	cm.m.Broadcast(Push{Ev: "call", A: c.Account, D: c.View()})
}

func (cm *CallManager) active() *Call {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for _, c := range cm.calls {
		if !c.ended && c.State != "ringing" {
			return c
		}
	}
	return nil
}

// activeFor es la llamada en curso de una cuenta (para "ocupado" y choques).
func (cm *CallManager) activeFor(a *Account) *Call {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for _, c := range cm.calls {
		if c.a == a && !c.ended && c.State != "ringing" {
			return c
		}
	}
	return nil
}

func (cm *CallManager) get(id string) *Call {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.calls[id]
}

func (cm *CallManager) List() []*CallView {
	cm.mu.Lock()
	list := make([]*Call, 0, len(cm.calls))
	for _, c := range cm.calls {
		if !c.ended {
			list = append(list, c)
		}
	}
	cm.mu.Unlock()
	out := make([]*CallView, 0, len(list))
	for _, c := range list {
		out = append(out, c.View())
	}
	return out
}

// --- servidores TURN/STUN ---

type iceServers struct {
	URIs     []string `json:"urls"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"credential,omitempty"`
}

func (a *Account) turnServers(ctx context.Context) *iceServers {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := a.cli.TurnServer(ctx)
	if err != nil || resp == nil || len(resp.URIs) == 0 {
		return nil
	}
	return &iceServers{URIs: resp.URIs, Username: resp.Username, Password: resp.Password}
}

// gstICE convierte las URIs de Matrix al formato de webrtcbin.
func gstICE(s *iceServers) (stun string, turns []string) {
	if s == nil {
		return "", nil
	}
	for _, u := range s.URIs {
		scheme, rest, ok := strings.Cut(u, ":")
		if !ok {
			continue
		}
		hostport, query, _ := strings.Cut(rest, "?")
		switch scheme {
		case "turn", "turns":
			ui := url.UserPassword(s.Username, s.Password).String()
			t := fmt.Sprintf("%s://%s@%s", scheme, ui, hostport)
			if query != "" {
				t += "?" + query
			}
			turns = append(turns, t)
			if stun == "" && scheme == "turn" {
				stun = "stun://" + hostport
			}
		case "stun", "stuns":
			if stun == "" {
				stun = "stun://" + hostport
			}
		}
	}
	return
}

// --- inicio de llamadas ---

func (cm *CallManager) newCall(a *Account, roomID, callID string, outgoing, video bool) *Call {
	c := &Call{
		cm: cm, a: a, ID: callID, Account: a.UserID(), Room: roomID, Outgoing: outgoing, Video: video,
		partyID: a.cli.DeviceID.String(), StartedAt: time.Now().UnixMilli(), Speaking: map[string]bool{},
	}
	if s := a.RoomSummary(roomID); s != nil && s.DM != "" {
		c.Peer = s.DM
		p := a.memberProfile(roomID, s.DM)
		c.PeerName, c.PeerAvatar = p.Name, p.Avatar
	} else if s != nil {
		c.PeerName, c.PeerAvatar = s.Name, s.Avatar
	}
	cm.mu.Lock()
	c.key = a.UserID() + "|" + callID
	cm.calls[c.key] = c
	cm.mu.Unlock()
	return c
}

func (cm *CallManager) engineFor(c *Call) (CallEngine, error) {
	s := cm.m.Settings()
	ice := c.a.turnServers(c.a.ctx)
	if cm.NativeFactory != nil && !cm.browserWebRTC {
		stun, turns := gstICE(ice)
		c.Engine = "native"
		return cm.NativeFactory(c, NativeEngineConfig{TURN: turns, STUN: stun, ForceRelay: s.CallForceRelay, Settings: s})
	}
	if cm.browserWebRTC || cm.m.HasSubscribers() {
		c.Engine = "browser"
		return newJSEngine(cm, c, ice, s), nil
	}
	return nil, errors.New("no hay motor de llamadas disponible")
}

// Place inicia una llamada saliente en una sala (1:1).
func (cm *CallManager) Place(a *Account, roomID string, video, muted, deaf bool) (*Call, error) {
	if cm.m.Policy().Disabled("calls") {
		return nil, errors.New("tu organización desactivó las llamadas")
	}
	if c := cm.active(); c != nil {
		return nil, errors.New("ya estás en una llamada")
	}
	s := a.RoomSummary(roomID)
	if s == nil || s.Membership != "join" {
		return nil, errors.New("sala desconocida")
	}
	if s.DM == "" && s.Joined > 2 {
		return nil, errors.New("group")
	}
	callID := fmt.Sprintf("hv%d%s", time.Now().UnixNano(), strings.ToLower(randomDeviceID()[:4]))
	c := cm.newCall(a, roomID, callID, true, video)
	c.State = "inviting"
	c.Muted, c.Deafened = muted, deaf
	eng, err := cm.engineFor(c)
	if err != nil {
		cm.end(c, "error", false)
		return nil, err
	}
	c.engine = eng
	cm.push(c)
	go func() {
		sdp, err := eng.Offer(video)
		if err != nil {
			cm.m.Log.Error().Err(err).Msg("No se pudo crear la oferta")
			cm.Hangup(c.key, "user_media_failed")
			return
		}
		content := map[string]any{
			"call_id": c.ID, "party_id": c.partyID, "version": "1", "lifetime": int(callLifetime.Milliseconds()),
			"offer": map[string]any{"type": "offer", "sdp": sdp},
		}
		if c.Peer != "" {
			content["invitee"] = c.Peer
		}
		if md := streamMetadata(sdp, c); md != nil {
			content["sdp_stream_metadata"] = md
		}
		if err := c.send(event.CallInvite, content); err != nil {
			cm.m.Log.Error().Err(err).Msg("No se pudo enviar la invitación de llamada")
			cm.end(c, "send_failed", false)
			return
		}
		c.mu.Lock()
		c.inviteSent = true
		pend := c.pendingOut
		c.pendingOut = nil
		c.timeout = time.AfterFunc(callLifetime, func() {
			if c.State == "inviting" {
				cm.Hangup(c.key, "invite_timeout")
			}
		})
		c.mu.Unlock()
		if len(pend) > 0 {
			c.sendCandidates(pend)
		}
	}()
	return c, nil
}

// streamMetadata marca el flujo principal como "m.usermedia" (Element lo usa para ubicar los videos).
func streamMetadata(sdp string, c *Call) map[string]any {
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "a=msid:") {
			stream := strings.Fields(strings.TrimPrefix(line, "a=msid:"))
			if len(stream) > 0 && stream[0] != "-" {
				return map[string]any{stream[0]: map[string]any{"purpose": "m.usermedia", "audio_muted": c.Muted, "video_muted": c.VideoOff}}
			}
		}
	}
	return nil
}

func (c *Call) send(t event.Type, content map[string]any) error {
	ctx, cancel := context.WithTimeout(c.a.ctx, 30*time.Second)
	defer cancel()
	_, err := c.a.sendWithEncryption(ctx, c.Room, t, content, "hvc"+c.a.cli.TxnID())
	return err
}

// --- candidatos ICE salientes (agrupados para no inundar la sala) ---

func (c *Call) localCandidate(cand CallCandidate) {
	c.mu.Lock()
	c.pendingOut = append(c.pendingOut, cand)
	if !c.inviteSent {
		// Los candidatos van después de la invitación o la respuesta.
		c.mu.Unlock()
		return
	}
	if c.outTimer == nil {
		c.outTimer = time.AfterFunc(150*time.Millisecond, func() {
			c.mu.Lock()
			pend := c.pendingOut
			c.pendingOut = nil
			c.outTimer = nil
			c.mu.Unlock()
			c.sendCandidates(pend)
		})
	}
	c.mu.Unlock()
}

func (c *Call) sendCandidates(cands []CallCandidate) {
	if len(cands) == 0 || c.ended {
		return
	}
	list := make([]map[string]any, 0, len(cands))
	for _, cd := range cands {
		m := map[string]any{"candidate": cd.Candidate}
		if cd.SDPMid != "" {
			m["sdpMid"] = cd.SDPMid
		}
		if cd.SDPMLineIndex != nil {
			m["sdpMLineIndex"] = *cd.SDPMLineIndex
		}
		list = append(list, m)
	}
	_ = c.send(event.CallCandidates, map[string]any{"call_id": c.ID, "party_id": c.partyID, "version": "1", "candidates": list})
}

// --- acciones del usuario ---

func (cm *CallManager) Answer(id string, video, muted, deaf bool) error {
	c := cm.get(id)
	if c == nil || c.State != "ringing" {
		return errors.New("no hay llamada para contestar")
	}
	if other := cm.active(); other != nil && other.ID != id {
		cm.Hangup(other.ID, "user_hangup")
	}
	c.mu.Lock()
	c.State = "connecting"
	c.Muted, c.Deafened = muted, deaf
	c.Video = video && c.inviteVideo
	if c.timeout != nil {
		c.timeout.Stop()
	}
	c.mu.Unlock()
	eng, err := cm.engineFor(c)
	if err != nil {
		cm.end(c, "error", false)
		return err
	}
	c.engine = eng
	cm.push(c)
	go func() {
		sdp, err := eng.Answer(c.offer, c.Video)
		if err != nil {
			cm.m.Log.Error().Err(err).Msg("No se pudo contestar la llamada")
			cm.Hangup(c.key, "user_media_failed")
			return
		}
		c.mu.Lock()
		c.remoteSet = true
		pend := c.pendingIn
		c.pendingIn = nil
		c.mu.Unlock()
		if len(pend) > 0 {
			eng.AddCandidates(pend)
		}
		content := map[string]any{"call_id": c.ID, "party_id": c.partyID, "version": "1",
			"answer": map[string]any{"type": "answer", "sdp": sdp}}
		if md := streamMetadata(sdp, c); md != nil {
			content["sdp_stream_metadata"] = md
		}
		if err := c.send(event.CallAnswer, content); err != nil {
			cm.end(c, "send_failed", false)
			return
		}
		c.mu.Lock()
		c.inviteSent = true
		out := c.pendingOut
		c.pendingOut = nil
		c.mu.Unlock()
		c.sendCandidates(out)
	}()
	return nil
}

func (cm *CallManager) Reject(id string) error {
	c := cm.get(id)
	if c == nil {
		return nil
	}
	go func() {
		_ = c.send(event.CallReject, map[string]any{"call_id": c.ID, "party_id": c.partyID, "version": "1"})
	}()
	cm.end(c, "rejected", false)
	return nil
}

func (cm *CallManager) Hangup(id, reason string) error {
	c := cm.get(id)
	if c == nil || c.ended {
		return nil
	}
	if reason == "" {
		reason = "user_hangup"
	}
	go func() {
		content := map[string]any{"call_id": c.ID, "party_id": c.partyID, "version": "1"}
		if reason != "user_hangup" {
			content["reason"] = reason
		}
		_ = c.send(event.CallHangup, content)
	}()
	cm.end(c, reason, false)
	return nil
}

func (cm *CallManager) end(c *Call, reason string, remote bool) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	missed := !c.Outgoing && c.State == "ringing" && remote && (reason == "remote_hangup" || reason == "invite_timeout")
	c.State = "ended"
	c.Reason = reason
	if c.timeout != nil {
		c.timeout.Stop()
	}
	if c.outTimer != nil {
		c.outTimer.Stop()
	}
	if c.discTimer != nil {
		c.discTimer.Stop()
	}
	eng := c.engine
	c.mu.Unlock()
	if eng != nil {
		eng.Close()
	}
	cm.push(c)
	if cm.m.Video != nil && cm.active() == nil {
		cm.m.Video.Clear()
	}
	if missed {
		cm.m.Notifier.Close(c.Account, c.Room)
		if cm.m.Settings().NotifyCalls && !c.a.isDND() {
			name := c.PeerName
			if name == "" {
				name = c.Peer
			}
			cm.m.Notifier.Notify(c.a, NotifyItem{Title: name, Body: "Llamada perdida", Kind: "call", Room: c.Room})
		}
	} else if !c.Outgoing {
		cm.m.Notifier.Close(c.Account, c.Room)
	}
	if cm.OnEnded != nil {
		cm.OnEnded(c)
	}
	time.AfterFunc(5*time.Second, func() {
		cm.mu.Lock()
		delete(cm.calls, c.key)
		cm.mu.Unlock()
	})
}

func (cm *CallManager) SetMuted(id string, muted bool) {
	if c := cm.get(id); c != nil && c.engine != nil {
		c.mu.Lock()
		c.Muted = muted
		c.mu.Unlock()
		c.engine.SetMicMuted(muted)
		cm.push(c)
		c.sendMetadata()
	}
}

func (cm *CallManager) SetVideoOff(id string, off bool) {
	if c := cm.get(id); c != nil && c.engine != nil {
		c.mu.Lock()
		c.VideoOff = off
		c.mu.Unlock()
		c.engine.SetVideoMuted(off)
		cm.push(c)
		c.sendMetadata()
	}
}

func (cm *CallManager) SetDeafened(id string, deaf bool) {
	if c := cm.get(id); c != nil && c.engine != nil {
		c.mu.Lock()
		c.Deafened = deaf
		c.mu.Unlock()
		s := cm.m.Settings()
		spk := uiFloat(s, "speakerVolume", 100) / 100
		if deaf {
			spk = 0
		}
		c.engine.SetVolumes(-1, spk)
		cm.push(c)
	}
}

func (cm *CallManager) Screen(id string, on bool, o ScreenOptions) error {
	c := cm.get(id)
	if c == nil || c.engine == nil || c.ended {
		return errors.New("no hay llamada")
	}
	o.normalize()
	if on && cm.m.Policy().Disabled("screenshare") {
		return errors.New("tu organización desactivó compartir pantalla")
	}
	if on {
		c.mu.Lock()
		already := c.Sharing
		c.mu.Unlock()
		c.mu.Lock()
		prev := c.Share
		c.mu.Unlock()
		switch {
		case already && prev != nil && prev.Source == o.Source:
			c.engine.ScreenQuality(o)
		case already:
			// Otra pantalla o ventana: se cambia la captura sin cortar la transmisión.
			c.engine.StopScreen()
			if err := c.engine.StartScreen(o); err != nil {
				c.mu.Lock()
				c.Sharing, c.Share = false, nil
				c.mu.Unlock()
				cm.push(c)
				return err
			}
		default:
			if err := c.engine.StartScreen(o); err != nil {
				return err
			}
		}
	} else {
		c.engine.StopScreen()
	}
	c.mu.Lock()
	c.Sharing = on
	if on {
		c.Share = &o
	} else {
		c.Share = nil
	}
	c.mu.Unlock()
	cm.push(c)
	c.sendMetadata()
	return nil
}

// SetDevice cambia micrófono, altavoz o cámara en plena llamada.
func (cm *CallManager) SetDevice(kind, id string) {
	for _, c := range cm.liveCalls() {
		if err := c.engine.SetDevice(kind, id); err != nil {
			cm.m.Log.Warn().Err(err).Str("kind", kind).Msg("No se pudo cambiar el dispositivo")
		}
	}
}

func (cm *CallManager) liveCalls() []*Call {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	var out []*Call
	for _, c := range cm.calls {
		if !c.ended && c.engine != nil {
			out = append(out, c)
		}
	}
	return out
}

// SettingsChanged aplica en vivo los cambios de dispositivos y volúmenes.
func (cm *CallManager) SettingsChanged(old, s Settings) {
	calls := cm.liveCalls()
	if len(calls) == 0 {
		return
	}
	if old.CallMic != s.CallMic {
		cm.SetDevice("mic", s.CallMic)
	}
	if old.CallSpeaker != s.CallSpeaker {
		cm.SetDevice("speaker", s.CallSpeaker)
	}
	if old.CallCamera != s.CallCamera {
		cm.SetDevice("camera", s.CallCamera)
	}
	mic, spk := uiFloat(s, "micVolume", 100)/100, uiFloat(s, "speakerVolume", 100)/100
	if mic != uiFloat(old, "micVolume", 100)/100 || spk != uiFloat(old, "speakerVolume", 100)/100 {
		for _, c := range calls {
			c.mu.Lock()
			deaf := c.Deafened
			c.mu.Unlock()
			if deaf {
				spk = 0
			}
			c.engine.SetVolumes(mic, spk)
		}
	}
	for _, c := range calls {
		if v := userVolume(s, c.Peer); v != userVolume(old, c.Peer) {
			c.engine.SetRemoteVolume(v)
		}
	}
}

// userVolume es el volumen que elegiste para una persona (0–200 %).
func userVolume(s Settings, user string) float64 {
	raw, ok := s.UI["userVolumes"]
	if !ok || user == "" {
		return 1
	}
	var m map[string]float64
	if json.Unmarshal(raw, &m) != nil {
		return 1
	}
	if v, ok := m[user]; ok {
		return max(0, min(v, 200)) / 100
	}
	return 1
}

// sendMetadata avisa al otro lado si el micrófono/cámara están apagados (v1).
func (c *Call) sendMetadata() {
	if c.State != "connected" {
		return
	}
	go func() {
		_ = c.send(sdpMetadataChanged, map[string]any{
			"call_id": c.ID, "party_id": c.partyID, "version": "1",
			"sdp_stream_metadata": map[string]any{"hilvan-" + c.partyID: map[string]any{"purpose": "m.usermedia", "audio_muted": c.Muted, "video_muted": c.VideoOff}},
		})
	}()
}

func uiFloat(s Settings, key string, def float64) float64 {
	if raw, ok := s.UI[key]; ok {
		if v, err := strconv.ParseFloat(string(raw), 64); err == nil {
			return v
		}
	}
	return def
}

func uiBool(s Settings, key string, def bool) bool {
	if raw, ok := s.UI[key]; ok {
		return string(raw) == "true"
	}
	return def
}

func uiString(s Settings, key, def string) string {
	if raw, ok := s.UI[key]; ok {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			return v
		}
		return strings.Trim(string(raw), `"`)
	}
	return def
}

// --- eventos m.call.* recibidos ---

func (cm *CallManager) HandleRoomEvent(a *Account, row *EventRow) {
	content := gjson.ParseBytes(row.Content)
	callID := content.Get("call_id").Str
	party := content.Get("party_id").Str
	if callID == "" {
		return
	}
	me := a.UserID()
	ours := row.Sender == me && party == a.cli.DeviceID.String()
	if ours {
		return // eco de nuestro propio evento
	}
	c := cm.get(a.UserID() + "|" + callID)
	switch row.Type {
	case "m.call.invite":
		if row.Sender == me {
			return // otro dispositivo nuestro llamando
		}
		if inv := content.Get("invitee").Str; inv != "" && inv != me {
			return
		}
		life := content.Get("lifetime").Int()
		if life <= 0 {
			life = callLifetime.Milliseconds()
		}
		age := time.Now().UnixMilli() - row.TS
		if age > life {
			return
		}
		if c != nil {
			return
		}
		// Choque: los dos llamamos a la vez en la misma sala (solo entre esta cuenta y quien llama;
		// con varias cuentas abiertas, la llamada de otra cuenta no cuenta).
		if act := cm.activeFor(a); act != nil {
			if act.Room == row.RoomID && act.Outgoing && act.State == "inviting" && (act.Peer == "" || act.Peer == row.Sender) {
				if row.Sender < me {
					cm.end(act, "replaced", false)
				} else {
					return
				}
			} else {
				// Ocupados: avisamos al otro lado.
				go a.sendWithEncryption(a.ctx, row.RoomID, event.CallHangup, map[string]any{
					"call_id": callID, "party_id": a.cli.DeviceID.String(), "version": "1", "reason": "user_busy"}, "hvc"+a.cli.TxnID())
				return
			}
		}
		sdp := content.Get("offer.sdp").Str
		nc := cm.newCall(a, row.RoomID, callID, false, strings.Contains(sdp, "m=video"))
		nc.inviteVideo = nc.Video
		nc.offer = sdp
		nc.peerParty = party
		nc.Peer = row.Sender
		p := a.memberProfile(row.RoomID, row.Sender)
		nc.PeerName, nc.PeerAvatar = p.Name, p.Avatar
		nc.State = "ringing"
		nc.timeout = time.AfterFunc(time.Duration(life-age)*time.Millisecond, func() {
			if nc.State == "ringing" {
				cm.end(nc, "invite_timeout", true)
			}
		})
		cm.push(nc)
		if cm.m.Settings().NotifyCalls && !a.isDND() {
			name := nc.PeerName
			if name == "" {
				name = row.Sender
			}
			kind := "Llamada de voz"
			if nc.Video {
				kind = "Videollamada"
			}
			cm.m.Notifier.Notify(a, NotifyItem{Title: name, Body: kind + " entrante", Kind: "call", Room: row.RoomID, Urgent: true})
		}
	case "m.call.answer":
		if c == nil {
			return
		}
		if row.Sender == me {
			// Otro dispositivo nuestro contestó.
			if c.State == "ringing" {
				cm.end(c, "answered_elsewhere", true)
			}
			return
		}
		if !c.Outgoing || c.State != "inviting" {
			return
		}
		c.mu.Lock()
		c.peerParty = party
		c.State = "connecting"
		if c.timeout != nil {
			c.timeout.Stop()
		}
		c.mu.Unlock()
		cm.push(c)
		go func() {
			_ = c.send(event.CallSelectAnswer, map[string]any{"call_id": c.ID, "party_id": c.partyID, "version": "1", "selected_party_id": party})
		}()
		if err := c.engine.SetAnswer(content.Get("answer.sdp").Str); err != nil {
			cm.m.Log.Error().Err(err).Msg("Respuesta de llamada inválida")
			cm.Hangup(c.key, "ice_failed")
			return
		}
		c.mu.Lock()
		c.remoteSet = true
		pend := c.pendingIn
		c.pendingIn = nil
		c.mu.Unlock()
		if len(pend) > 0 {
			c.engine.AddCandidates(pend)
		}
	case "m.call.candidates":
		if c == nil || row.Sender == me {
			return
		}
		if c.peerParty != "" && party != "" && party != c.peerParty {
			return
		}
		var cands []CallCandidate
		_ = json.Unmarshal([]byte(content.Get("candidates").Raw), &cands)
		c.mu.Lock()
		if !c.remoteSet || c.engine == nil {
			c.pendingIn = append(c.pendingIn, cands...)
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		c.engine.AddCandidates(cands)
	case "m.call.select_answer":
		if c == nil || c.Outgoing {
			return
		}
		if content.Get("selected_party_id").Str != c.partyID {
			cm.end(c, "answered_elsewhere", true)
		}
	case "m.call.hangup", "m.call.reject":
		if c == nil {
			return
		}
		if row.Sender == me && row.Type == "m.call.reject" && c.State == "ringing" {
			cm.end(c, "rejected_elsewhere", true)
			return
		}
		if c.peerParty != "" && party != "" && party != c.peerParty && row.Sender != me {
			return
		}
		reason := content.Get("reason").Str
		if row.Type == "m.call.reject" {
			reason = "rejected"
		}
		if reason == "" {
			reason = "remote_hangup"
		}
		cm.end(c, reason, true)
	case "m.call.negotiate":
		if c == nil || c.engine == nil || row.Sender == me {
			return
		}
		desc := content.Get("description")
		switch desc.Get("type").Str {
		case "offer":
			go func() {
				ans, err := c.engine.Renegotiate(desc.Get("sdp").Str)
				if err != nil {
					cm.m.Log.Warn().Err(err).Msg("Renegociación fallida")
					return
				}
				_ = c.send(event.CallNegotiate, map[string]any{"call_id": c.ID, "party_id": c.partyID, "version": "1",
					"lifetime": int(callLifetime.Milliseconds()), "description": map[string]any{"type": "answer", "sdp": ans}})
			}()
		case "answer":
			if err := c.engine.SetAnswer(desc.Get("sdp").Str); err != nil {
				cm.m.Log.Warn().Err(err).Msg("Respuesta de renegociación inválida")
			}
		}
	case "m.call.sdp_stream_metadata_changed", "org.matrix.call.sdp_stream_metadata_changed":
		if c == nil {
			return
		}
		md := content.Get("sdp_stream_metadata")
		videoMuted := false
		md.ForEach(func(_, v gjson.Result) bool {
			if v.Get("purpose").Str == "m.usermedia" {
				videoMuted = v.Get("video_muted").Bool()
			}
			return true
		})
		c.mu.Lock()
		c.RemoteVid = !videoMuted
		c.mu.Unlock()
		cm.push(c)
	}
}

// --- callbacks del motor ---

func (c *Call) engineState(kind, state string) {
	cm := c.cm
	switch {
	case (kind == "connection" && state == "connected") || (kind == "ice" && (state == "connected" || state == "completed")):
		c.mu.Lock()
		if c.discTimer != nil {
			c.discTimer.Stop()
			c.discTimer = nil
		}
		first := c.State != "connected"
		c.State = "connected"
		if c.ConnectAt == 0 {
			c.ConnectAt = time.Now().UnixMilli()
		}
		c.mu.Unlock()
		if first {
			cm.push(c)
		}
	case kind == "connection" && state == "failed", kind == "ice" && state == "failed":
		cm.Hangup(c.key, "ice_failed")
	case kind == "connection" && state == "disconnected":
		// Puede volver solo (cambio de red); si no vuelve en 12 s, se corta.
		c.mu.Lock()
		if c.discTimer == nil {
			c.discTimer = time.AfterFunc(12*time.Second, func() {
				c.mu.Lock()
				c.discTimer = nil
				c.mu.Unlock()
				cm.Hangup(c.key, "ice_failed")
			})
		}
		c.mu.Unlock()
	case kind == "share-error":
		cm.m.Log.Warn().Str("call", c.ID).Str("error", state).Msg("Se cortó la transmisión de pantalla")
		go func() {
			_ = cm.Screen(c.key, false, ScreenOptions{})
			cm.m.Broadcast(Push{Ev: "call.notice", A: c.Account, D: map[string]any{"id": c.key, "text": "La transmisión se detuvo: " + state}})
		}()
	case kind == "error":
		cm.m.Log.Error().Str("call", c.ID).Str("error", state).Msg("Error en el motor de la llamada")
		if c.State != "connected" {
			cm.Hangup(c.key, "user_media_failed")
		}
	}
}

func (c *Call) engineStream(stream, kind string) {
	c.mu.Lock()
	found := false
	for _, s := range c.Streams {
		if s == stream {
			found = true
		}
	}
	if !found {
		c.Streams = append(c.Streams, stream)
	}
	if kind == "video" && strings.HasPrefix(stream, "remote") {
		c.RemoteVid = true
	}
	c.mu.Unlock()
	if kind == "video" {
		c.cm.attachStream(c, stream)
	}
	if c.cm.OnStream != nil {
		c.cm.OnStream(c, stream)
	}
	c.cm.push(c)
}

var speakingThreshold = -45.0

func (c *Call) engineLevel(who string, rms float64) {
	speaking := rms > speakingThreshold
	c.mu.Lock()
	changed := c.Speaking[who] != speaking
	c.Speaking[who] = speaking
	c.mu.Unlock()
	if changed {
		c.cm.m.Broadcast(Push{Ev: "call.speaking", A: c.Account, D: map[string]any{"id": c.key, "who": who, "speaking": speaking}})
	}
}

// --- motor del navegador (WebRTC de la interfaz) ---

type jsReply struct {
	Result json.RawMessage
	Err    string
}

type jsEngine struct {
	cm  *CallManager
	c   *Call
	ice *iceServers
	s   Settings
}

func newJSEngine(cm *CallManager, c *Call, ice *iceServers, s Settings) *jsEngine {
	return &jsEngine{cm: cm, c: c, ice: ice, s: s}
}

func (j *jsEngine) request(op string, args map[string]any) (json.RawMessage, error) {
	cm := j.cm
	cm.mu.Lock()
	cm.jsSeq++
	req := strconv.Itoa(cm.jsSeq)
	ch := make(chan jsReply, 1)
	cm.jsWaiters[req] = ch
	cm.mu.Unlock()
	defer func() {
		cm.mu.Lock()
		delete(cm.jsWaiters, req)
		cm.mu.Unlock()
	}()
	if args == nil {
		args = map[string]any{}
	}
	args["call"] = j.c.key
	args["op"] = op
	args["req"] = req
	cm.m.Broadcast(Push{Ev: "call.engine", A: j.c.Account, D: args})
	select {
	case r := <-ch:
		if r.Err != "" {
			return nil, errors.New(r.Err)
		}
		return r.Result, nil
	case <-time.After(45 * time.Second):
		return nil, errors.New("la interfaz no respondió")
	}
}

func (j *jsEngine) config() map[string]any {
	ice := []any{}
	if j.ice != nil {
		ice = append(ice, j.ice)
	}
	return map[string]any{"ice": ice, "relay": j.s.CallForceRelay, "mic": j.s.CallMic, "camera": j.s.CallCamera,
		"ns": j.s.CallNoiseSuppress, "ec": j.s.CallEchoCancel, "low_latency": j.s.CallLowLatency,
		"muted": j.c.Muted, "deafened": j.c.Deafened}
}

func sdpFrom(raw json.RawMessage, err error) (string, error) {
	if err != nil {
		return "", err
	}
	var r struct {
		SDP string `json:"sdp"`
	}
	_ = json.Unmarshal(raw, &r)
	if r.SDP == "" {
		return "", errors.New("SDP vacío")
	}
	return r.SDP, nil
}

func (j *jsEngine) Offer(video bool) (string, error) {
	cfg := j.config()
	cfg["video"] = video
	return sdpFrom(j.request("offer", cfg))
}

func (j *jsEngine) Answer(offer string, video bool) (string, error) {
	cfg := j.config()
	cfg["video"] = video
	cfg["sdp"] = offer
	return sdpFrom(j.request("answer", cfg))
}

func (j *jsEngine) SetAnswer(sdp string) error {
	_, err := j.request("setAnswer", map[string]any{"sdp": sdp})
	return err
}

func (j *jsEngine) Renegotiate(offer string) (string, error) {
	return sdpFrom(j.request("renegotiate", map[string]any{"sdp": offer}))
}

func (j *jsEngine) AddCandidates(c []CallCandidate) {
	go func() { _, _ = j.request("candidates", map[string]any{"candidates": c}) }()
}

func (j *jsEngine) SetMicMuted(m bool) {
	go func() { _, _ = j.request("mute", map[string]any{"muted": m}) }()
}

func (j *jsEngine) SetVideoMuted(m bool) {
	go func() { _, _ = j.request("videoOff", map[string]any{"off": m}) }()
}

func (j *jsEngine) SetVolumes(mic, spk float64) {
	go func() { _, _ = j.request("volume", map[string]any{"spk": spk}) }()
}

func (j *jsEngine) StartScreen(o ScreenOptions) error {
	_, err := j.request("screen", map[string]any{"on": true, "opts": o})
	return err
}

func (j *jsEngine) ScreenQuality(o ScreenOptions) {
	go func() { _, _ = j.request("screenQuality", map[string]any{"opts": o}) }()
}

func (j *jsEngine) StopScreen() {
	go func() { _, _ = j.request("screen", map[string]any{"on": false}) }()
}

func (j *jsEngine) SetDevice(kind, id string) error { return nil } // el navegador usa sus propios dispositivos
func (j *jsEngine) SetRemoteVolume(v float64) {
	go func() { _, _ = j.request("volume", map[string]any{"spk": v}) }()
}

func (j *jsEngine) Stats() any {
	r, err := j.request("stats", nil)
	if err != nil {
		return nil
	}
	return r
}

func (j *jsEngine) Close() {
	j.cm.m.Broadcast(Push{Ev: "call.engine", A: j.c.Account, D: map[string]any{"call": j.c.key, "op": "close"}})
}

// EngineReply recibe la respuesta de la interfaz a un pedido del motor del navegador.
func (cm *CallManager) EngineReply(req string, result json.RawMessage, errMsg string) {
	cm.mu.Lock()
	ch := cm.jsWaiters[req]
	cm.mu.Unlock()
	if ch != nil {
		ch <- jsReply{Result: result, Err: errMsg}
	}
}

// EngineEvent recibe eventos espontáneos del motor del navegador (candidatos, estado, flujos).
func (cm *CallManager) EngineEvent(callID, kind string, data json.RawMessage) {
	c := cm.get(callID)
	if c == nil {
		return
	}
	switch kind {
	case "candidate":
		var cand CallCandidate
		if json.Unmarshal(data, &cand) == nil {
			c.localCandidate(cand)
		}
	case "state":
		var st struct{ Kind, State string }
		_ = json.Unmarshal(data, &st)
		c.engineState(st.Kind, st.State)
	case "stream":
		var st struct{ Stream, Kind string }
		_ = json.Unmarshal(data, &st)
		c.engineStream(st.Stream, st.Kind)
	case "level":
		var lv struct {
			Who string
			RMS float64
		}
		_ = json.Unmarshal(data, &lv)
		c.engineLevel(lv.Who, lv.RMS)
	case "negotiation":
		go c.renegotiateLocal()
	}
}

// renegotiateLocal envía una nueva oferta (p. ej. al agregar la pantalla compartida).
func (c *Call) renegotiateLocal() {
	if c.State != "connected" || c.engine == nil {
		return
	}
	j, ok := c.engine.(interface{ LocalOffer() (string, error) })
	if !ok {
		return
	}
	sdp, err := j.LocalOffer()
	if err != nil {
		return
	}
	_ = c.send(event.CallNegotiate, map[string]any{"call_id": c.ID, "party_id": c.partyID, "version": "1",
		"lifetime": int(callLifetime.Milliseconds()), "description": map[string]any{"type": "offer", "sdp": sdp}})
}

func (j *jsEngine) LocalOffer() (string, error) { return sdpFrom(j.request("localOffer", nil)) }

// SetBrowserWebRTC lo informa la interfaz al conectarse.
func (cm *CallManager) SetBrowserWebRTC(ok bool) { cm.browserWebRTC = ok }
