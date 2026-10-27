package core

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"hilvan/internal/media"
)

// Motor de llamadas nativo (GStreamer webrtcbin) para la señalización de voip.go.

type nativeEngine struct {
	c          *Call
	cfg        media.CallConfig
	nc         *media.NativeCall
	mu         sync.Mutex
	mids       []string // a=mid por m-line de la descripción local
	share      *screenCast
	shareAudio bool
	openVideo  bool // abrir el video cuando se acepte la renegociación
}

// EnableNativeCalls activa el motor nativo (lo llama main cuando hay GStreamer).
func (cm *CallManager) EnableNativeCalls(headless bool) {
	if !media.Available() {
		return
	}
	cm.NativeFactory = func(c *Call, ec NativeEngineConfig) (CallEngine, error) {
		return newNativeEngine(c, ec, headless), nil
	}
}

func newNativeEngine(c *Call, ec NativeEngineConfig, headless bool) *nativeEngine {
	s := ec.Settings
	profile := uiString(s, "inputProfile", "isolation")
	ns, echo, agc := s.CallNoiseSuppress, s.CallEchoCancel, uiBool(s, "agc", true)
	nsLevel := int(uiFloat(s, "noiseLevel", 2))
	switch profile {
	case "isolation":
		ns, echo, agc, nsLevel = true, true, true, 3
	case "studio":
		ns, echo, agc = false, false, false
	}
	w, h := 1280, 720
	switch uiString(s, "videoQuality", "auto") {
	case "480":
		w, h = 854, 480
	case "1080":
		w, h = 1920, 1080
	}
	kbps := 1500
	if h >= 1080 {
		kbps = 3500
	} else if h <= 480 {
		kbps = 700
	}
	cfg := media.CallConfig{
		ID: c.ID + "/" + c.partyID, Mic: s.CallMic, Speaker: s.CallSpeaker, Camera: s.CallCamera,
		TestMedia: os.Getenv("HILVAN_CALL_TEST_MEDIA") == "1", Headless: headless,
		STUN: ec.STUN, TURN: ec.TURN, ForceRelay: ec.ForceRelay,
		NoiseSuppression: ns, NoiseLevel: nsLevel, EchoCancel: echo, AGC: agc,
		MicGain: uiFloat(s, "micVolume", 100) / 100, SpkGain: uiFloat(s, "speakerVolume", 100) / 100,
		LowLatency: s.CallLowLatency, Width: w, Height: h, FPS: 30, VideoKbps: kbps, AudioBitrate: 48000,
	}
	return &nativeEngine{c: c, cfg: cfg}
}

func (e *nativeEngine) start(video bool) error {
	e.cfg.Video = video
	nc, err := media.NewCall(e.cfg, media.CallEvents{
		Candidate: func(mline int, cand string) {
			if mline < 0 {
				return // fin de candidatos: lo indicamos con el SDP completo
			}
			idx := mline
			e.c.localCandidate(CallCandidate{Candidate: cand, SDPMid: e.mid(mline), SDPMLineIndex: &idx})
		},
		State:             func(kind, state string) { e.c.engineState(kind, state) },
		NegotiationNeeded: func() {},
		Stream: func(stream, kind string) {
			e.c.engineStream(stream, kind)
		},
		Level: func(who string, rms float64) { e.c.engineLevel(who, rms) },
	})
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.nc = nc
	e.mu.Unlock()
	return nil
}

func (e *nativeEngine) mid(mline int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if mline < len(e.mids) {
		return e.mids[mline]
	}
	return strconv.Itoa(mline)
}

func (e *nativeEngine) setLocal(sdp string) {
	e.mu.Lock()
	e.mids = sdpMids(sdp)
	e.mu.Unlock()
}

// sdpMids devuelve el a=mid de cada m-line.
func sdpMids(sdp string) []string {
	var mids []string
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=") {
			mids = append(mids, strconv.Itoa(len(mids)))
		} else if strings.HasPrefix(line, "a=mid:") && len(mids) > 0 {
			mids[len(mids)-1] = strings.TrimPrefix(line, "a=mid:")
		}
	}
	return mids
}

// sdpMedia busca la m-line de un tipo y el payload de un códec (p. ej. "opus", "VP8").
func sdpMedia(sdp, kind, codec string) (mline, pt int) {
	mline, pt = -1, -1
	idx := -1
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=") {
			idx++
			if mline < 0 && strings.HasPrefix(line, "m="+kind+" ") {
				mline = idx
			}
			continue
		}
		if idx == mline && mline >= 0 && pt < 0 && strings.HasPrefix(line, "a=rtpmap:") {
			f := strings.Fields(strings.TrimPrefix(line, "a=rtpmap:"))
			if len(f) == 2 && strings.EqualFold(strings.Split(f[1], "/")[0], codec) {
				pt, _ = strconv.Atoi(f[0])
			}
		}
	}
	return
}

func (e *nativeEngine) Offer(video bool) (string, error) {
	e.cfg.AudioPT, e.cfg.VideoPT = 111, 96
	if err := e.start(video); err != nil {
		return "", err
	}
	vline := -1
	if video {
		vline = 1
	}
	if err := e.nc.LinkSources(0, vline); err != nil {
		return "", err
	}
	if video {
		e.c.engineStream("local", "video")
	}
	if err := e.nc.Play(); err != nil {
		return "", err
	}
	e.applyJoinPrefs()
	sdp, err := e.nc.Create(true)
	if err == nil {
		e.setLocal(sdp)
	}
	return sdp, err
}

func (e *nativeEngine) Answer(offer string, video bool) (string, error) {
	aline, apt := sdpMedia(offer, "audio", "opus")
	vline, vpt := sdpMedia(offer, "video", "VP8")
	if aline < 0 || apt < 0 {
		return "", errors.New("la otra parte no ofrece audio Opus")
	}
	e.cfg.AudioPT, e.cfg.VideoPT = apt, vpt
	sendVideo := video && vline >= 0 && vpt >= 0
	if err := e.start(sendVideo); err != nil {
		return "", err
	}
	// Primero nuestras pistas (transceptores sin asociar); al aplicar la oferta,
	// webrtcbin las empareja por tipo de medio con las m-lines remotas.
	v := -1
	if sendVideo {
		v = 1
	}
	if err := e.nc.LinkSources(0, v); err != nil {
		return "", err
	}
	if sendVideo {
		e.c.engineStream("local", "video")
	}
	if err := e.nc.Play(); err != nil {
		return "", err
	}
	if err := e.nc.SetRemote(true, offer); err != nil {
		return "", err
	}
	e.applyJoinPrefs()
	sdp, err := e.nc.Create(false)
	if err == nil {
		e.setLocal(sdp)
	}
	return sdp, err
}

// applyJoinPrefs aplica "entrar silenciado" y "entrar con la cámara apagada".
func (e *nativeEngine) applyJoinPrefs() {
	e.c.mu.Lock()
	muted, deaf := e.c.Muted, e.c.Deafened
	e.c.mu.Unlock()
	if muted {
		e.nc.SetMicMuted(true)
	}
	if deaf {
		e.nc.SetVolumes(-1, 0)
	}
	if v := userVolume(e.c.cm.m.Settings(), e.c.Peer); v != 1 {
		e.nc.SetRemoteVolume(v)
	}
}

func (e *nativeEngine) SetAnswer(sdp string) error {
	if err := e.nc.SetRemote(false, sdp); err != nil {
		return err
	}
	e.mu.Lock()
	open := e.openVideo
	e.openVideo = false
	e.mu.Unlock()
	if open {
		e.nc.SetVideoMuted(false)
	}
	return nil
}

func (e *nativeEngine) Renegotiate(offer string) (string, error) {
	if err := e.nc.SetRemote(true, offer); err != nil {
		return "", err
	}
	sdp, err := e.nc.Create(false)
	if err == nil {
		e.setLocal(sdp)
	}
	return sdp, err
}

func (e *nativeEngine) LocalOffer() (string, error) {
	sdp, err := e.nc.Create(true)
	if err == nil {
		e.setLocal(sdp)
	}
	return sdp, err
}

func (e *nativeEngine) AddCandidates(cands []CallCandidate) {
	for _, c := range cands {
		if c.Candidate == "" {
			continue
		}
		mline := 0
		if c.SDPMLineIndex != nil {
			mline = *c.SDPMLineIndex
		} else if n, err := strconv.Atoi(c.SDPMid); err == nil {
			mline = n
		} else {
			for i, m := range e.mids {
				if m == c.SDPMid {
					mline = i
				}
			}
		}
		e.nc.AddCandidate(mline, strings.TrimPrefix(c.Candidate, "a="))
	}
}

func (e *nativeEngine) SetMicMuted(m bool)   { e.nc.SetMicMuted(m) }
func (e *nativeEngine) SetVideoMuted(m bool) { e.nc.SetVideoMuted(m) }
func (e *nativeEngine) SetVolumes(mic, spk float64) {
	e.nc.SetVolumes(mic, spk)
}

func (e *nativeEngine) StartScreen(o ScreenOptions) error {
	s := e.c.cm.m.Settings()
	cursor := uiBool(s, "shareCursor", true)
	var sc *screenCast
	var err error
	switch {
	case strings.HasPrefix(o.Source, "monitor:"):
		sc, err = startMutterMonitor(strings.TrimPrefix(o.Source, "monitor:"), cursor)
	case o.Source == "window" || o.Source == "window:last":
		restore := ""
		if o.Source == "window:last" {
			restore = uiString(s, "shareRestoreWindow", "")
		}
		sc, err = startScreenCast(cursor, 2, restore)
		if err == nil && sc.restore != "" {
			go e.c.cm.m.UpdateSettings([]byte(`{"ui":{"shareRestoreWindow":` + strconv.Quote(sc.restore) + `}}`))
		}
	default:
		sc, err = startScreenCast(cursor, 1|2, "")
	}
	if err != nil {
		return err
	}
	w, h := o.size()
	renegotiate, err := e.nc.StartScreen(sc.fd, sc.node, o.FPS, w, h, int(o.Mbps*1000))
	if err != nil {
		sc.Close()
		return err
	}
	e.mu.Lock()
	e.share = sc
	e.mu.Unlock()
	sc.onClosed = func() { _ = e.c.cm.Screen(e.c.key, false, ScreenOptions{}) }
	if o.Audio != "" {
		target := o.Audio
		if target == "system" {
			target = ""
		}
		if err := e.nc.ShareAudio(target, o.AudioVolume); err != nil {
			e.c.cm.m.Log.Warn().Err(err).Msg("No se pudo compartir el audio")
		} else {
			e.mu.Lock()
			e.shareAudio = true
			e.mu.Unlock()
		}
	}
	if renegotiate {
		// Llamada de voz: la pista de video nueva se negocia con m.call.negotiate.
		e.mu.Lock()
		e.openVideo = true
		e.mu.Unlock()
		go e.c.renegotiateLocal()
	}
	return nil
}

func (e *nativeEngine) ScreenQuality(o ScreenOptions) {
	w, h := o.size()
	e.nc.ScreenQuality(o.FPS, w, h, int(o.Mbps*1000))
	e.mu.Lock()
	had := e.shareAudio
	e.mu.Unlock()
	switch {
	case o.Audio == "" && had:
		e.nc.StopShareAudio()
		e.mu.Lock()
		e.shareAudio = false
		e.mu.Unlock()
	case o.Audio != "" && !had:
		target := o.Audio
		if target == "system" {
			target = ""
		}
		if e.nc.ShareAudio(target, o.AudioVolume) == nil {
			e.mu.Lock()
			e.shareAudio = true
			e.mu.Unlock()
		}
	case o.Audio != "":
		e.nc.ShareAudioVolume(o.AudioVolume)
	}
}

func (e *nativeEngine) StopScreen() {
	e.mu.Lock()
	sc := e.share
	e.share = nil
	audio := e.shareAudio
	e.shareAudio = false
	e.mu.Unlock()
	if sc == nil {
		return
	}
	if audio {
		e.nc.StopShareAudio()
	}
	e.nc.StopScreen()
	sc.Close()
}

func (e *nativeEngine) SetDevice(kind, id string) error { return e.nc.SetDevice(kind, id) }
func (e *nativeEngine) SetRemoteVolume(v float64)       { e.nc.SetRemoteVolume(v) }

func (e *nativeEngine) Stats() any {
	if e.nc == nil {
		return nil
	}
	return parseGstStats(e.nc.Stats())
}

// VideoSink expone el sink de video de un flujo para que el shell tome su imagen.
func (e *nativeEngine) VideoSink(stream string) unsafe.Pointer {
	if e.nc == nil {
		return nil
	}
	return e.nc.VideoSink(stream)
}

func (e *nativeEngine) Close() {
	e.StopScreen()
	e.mu.Lock()
	nc := e.nc
	e.mu.Unlock()
	if nc != nil {
		nc.Close()
	}
}

// parseGstStats resume las estadísticas de webrtcbin (RTT, pérdida, bitrate).
func parseGstStats(raw string) map[string]any {
	raw = strings.ReplaceAll(raw, "\\", "")
	out := map[string]any{}
	grab := func(key string) (float64, bool) {
		i := strings.Index(raw, key+"=(")
		if i < 0 {
			return 0, false
		}
		rest := raw[i+len(key)+2:]
		j := strings.Index(rest, ")")
		if j < 0 {
			return 0, false
		}
		rest = rest[j+1:]
		end := strings.IndexAny(rest, ",;\\ ")
		if end < 0 {
			end = len(rest)
		}
		v, err := strconv.ParseFloat(rest[:end], 64)
		return v, err == nil
	}
	if v, ok := grab("round-trip-time"); ok {
		out["rtt_ms"] = int(v * 1000)
	}
	if v, ok := grab("packets-lost"); ok {
		out["lost"] = int(v)
	}
	if v, ok := grab("jitter"); ok {
		out["jitter_ms"] = fmt.Sprintf("%.1f", v*1000)
	}
	out["engine"] = "GStreamer"
	return out
}
