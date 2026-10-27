//go:build linux && !android

package media

/*
#include <stdlib.h>
#include <glib.h>
#include "media.h"
#include "call.h"
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"
)

// CallConfig configura una llamada nativa.
type CallConfig struct {
	ID                   string
	Mic, Speaker, Camera string
	Video                bool
	TestMedia            bool
	Headless             bool
	STUN                 string
	TURN                 []string
	ForceRelay           bool
	NoiseSuppression     bool
	NoiseLevel           int
	EchoCancel           bool
	AGC                  bool
	MicGain, SpkGain     float64
	LowLatency           bool
	Width, Height, FPS   int
	VideoKbps            int
	AudioBitrate         int
	AudioPT, VideoPT     int
}

// CallEvents recibe lo que pasa en la llamada (se invoca desde hilos de GStreamer).
type CallEvents struct {
	Candidate         func(mline int, cand string) // mline -1 = fin de candidatos
	State             func(kind, state string)
	NegotiationNeeded func()
	Stream            func(stream, kind string)
	Level             func(who string, rms float64)
}

type NativeCall struct {
	id     string
	c      *C.HvCall
	use    sync.RWMutex // protege c contra la liberación (Close)
	ev     CallEvents
	mu     sync.Mutex
	closed bool
	stop   chan struct{}
}

var (
	callsMu sync.Mutex
	calls   = map[string]*NativeCall{}
)

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

func NewCall(cfg CallConfig, ev CallEvents) (*NativeCall, error) {
	cs := func(s string) *C.char { return C.CString(s) }
	id, mic, spk, cam, stun := cs(cfg.ID), cs(cfg.Mic), cs(cfg.Speaker), cs(cfg.Camera), cs(cfg.STUN)
	defer func() {
		for _, p := range []*C.char{id, mic, spk, cam, stun} {
			C.free(unsafe.Pointer(p))
		}
	}()
	var turns **C.char
	cturns := make([]*C.char, len(cfg.TURN))
	for i, t := range cfg.TURN {
		cturns[i] = C.CString(t)
	}
	defer func() {
		for _, p := range cturns {
			C.free(unsafe.Pointer(p))
		}
	}()
	if len(cturns) > 0 {
		// El arreglo de punteros debe vivir en memoria C (no se pueden pasar punteros Go a punteros Go).
		arr := C.malloc(C.size_t(len(cturns)) * C.size_t(unsafe.Sizeof(uintptr(0))))
		defer C.free(arr)
		slice := unsafe.Slice((**C.char)(arr), len(cturns))
		copy(slice, cturns)
		turns = (**C.char)(arr)
	}
	ccfg := C.HvCallConfig{
		id: id, mic: mic, speaker: spk, camera: cam, video: cbool(cfg.Video), test_media: cbool(cfg.TestMedia),
		headless: cbool(cfg.Headless), stun: stun, turns: turns, n_turns: C.int(len(cfg.TURN)), force_relay: cbool(cfg.ForceRelay),
		ns: cbool(cfg.NoiseSuppression), ns_level: C.int(cfg.NoiseLevel), ec: cbool(cfg.EchoCancel), agc: cbool(cfg.AGC),
		mic_gain: C.double(cfg.MicGain), spk_gain: C.double(cfg.SpkGain), low_latency: cbool(cfg.LowLatency),
		video_w: C.int(cfg.Width), video_h: C.int(cfg.Height), video_fps: C.int(cfg.FPS), video_kbps: C.int(cfg.VideoKbps),
		audio_bitrate: C.int(cfg.AudioBitrate), audio_pt: C.int(cfg.AudioPT), video_pt: C.int(cfg.VideoPT),
	}
	nc := &NativeCall{id: cfg.ID, ev: ev, stop: make(chan struct{})}
	callsMu.Lock()
	calls[cfg.ID] = nc
	callsMu.Unlock()
	c := C.hv_call_new(&ccfg)
	if c == nil {
		callsMu.Lock()
		delete(calls, cfg.ID)
		callsMu.Unlock()
		return nil, errors.New("no se pudo crear el motor de la llamada (¿falta webrtcbin?)")
	}
	nc.c = c
	go nc.pollLoop()
	return nc, nil
}

// with ejecuta f con la llamada C mientras siga viva. Close espera a que terminen las que están
// en curso, así nunca se usa una llamada ya liberada (p. ej. compartir pantalla justo al colgar).
func (n *NativeCall) with(f func(c *C.HvCall)) bool {
	n.use.RLock()
	defer n.use.RUnlock()
	if n.c == nil {
		return false
	}
	f(n.c)
	return true
}

var errClosed = errors.New("la llamada ya terminó")

func (n *NativeCall) pollLoop() {
	for {
		select {
		case <-n.stop:
			return
		default:
		}
		done := false
		if !n.with(func(c *C.HvCall) { done = C.hv_call_poll(c, 100) < 0 }) || done {
			return
		}
	}
}

func (n *NativeCall) LinkSources(audioMLine, videoMLine int) error {
	ok := false
	if !n.with(func(c *C.HvCall) { ok = C.hv_call_link_sources(c, C.int(audioMLine), C.int(videoMLine)) != 0 }) {
		return errClosed
	}
	if !ok {
		return errors.New("no se pudieron abrir el micrófono o la cámara")
	}
	return nil
}

func (n *NativeCall) Play() error {
	ok := false
	if !n.with(func(c *C.HvCall) { ok = C.hv_call_play(c) != 0 }) {
		return errClosed
	}
	if !ok {
		return errors.New("no se pudo iniciar el audio/video")
	}
	return nil
}

func (n *NativeCall) Create(offer bool) (string, error) {
	var out string
	got := false
	if !n.with(func(c *C.HvCall) {
		s := C.hv_call_create(c, cbool(offer))
		if s != nil {
			got = true
			out = C.GoString(s)
			C.g_free(C.gpointer(unsafe.Pointer(s)))
		}
	}) {
		return "", errClosed
	}
	if !got {
		return "", errors.New("no se pudo negociar la llamada")
	}
	return out, nil
}

func (n *NativeCall) SetRemote(offer bool, sdp string) error {
	cs := C.CString(sdp)
	defer C.free(unsafe.Pointer(cs))
	ok := false
	if !n.with(func(c *C.HvCall) { ok = C.hv_call_set_remote(c, cbool(offer), cs) != 0 }) {
		return errClosed
	}
	if !ok {
		return errors.New("la descripción remota no es válida")
	}
	return nil
}

func (n *NativeCall) AddCandidate(mline int, cand string) {
	cs := C.CString(cand)
	defer C.free(unsafe.Pointer(cs))
	n.with(func(c *C.HvCall) { C.hv_call_add_candidate(c, C.int(mline), cs) })
}

func (n *NativeCall) SetMicMuted(m bool) {
	n.with(func(c *C.HvCall) { C.hv_call_set_mic_muted(c, cbool(m)) })
}

func (n *NativeCall) SetVideoMuted(m bool) {
	n.with(func(c *C.HvCall) { C.hv_call_set_video_muted(c, cbool(m)) })
}

func (n *NativeCall) SetVolumes(mic, spk float64) {
	n.with(func(c *C.HvCall) { C.hv_call_set_volumes(c, C.double(mic), C.double(spk)) })
}

func (n *NativeCall) SetBitrate(kbps int) {
	n.with(func(c *C.HvCall) { C.hv_call_set_bitrate(c, C.int(kbps)) })
}

// StartScreen empieza a enviar la pantalla (w/h 0 = resolución original).
// Devuelve renegotiate=true si hubo que agregar una pista de video (llamada de voz).
func (n *NativeCall) StartScreen(fd int, node uint32, fps, w, h, kbps int) (renegotiate bool, err error) {
	var r C.int
	if !n.with(func(c *C.HvCall) {
		r = C.hv_call_screen_start(c, C.int(fd), C.uint(node), C.int(fps), C.int(w), C.int(h), C.int(kbps))
	}) {
		return false, errClosed
	}
	if r == 0 {
		return false, errors.New("no se pudo iniciar la captura de pantalla")
	}
	return r == 2, nil
}

func (n *NativeCall) ScreenQuality(fps, w, h, kbps int) {
	n.with(func(c *C.HvCall) { C.hv_call_screen_quality(c, C.int(fps), C.int(w), C.int(h), C.int(kbps)) })
}

// ShareAudio mezcla audio del sistema (target "") o de una aplicación (número de serie de PipeWire).
func (n *NativeCall) ShareAudio(target string, gain float64) error {
	cs := C.CString(target)
	defer C.free(unsafe.Pointer(cs))
	ok := false
	if !n.with(func(c *C.HvCall) { ok = C.hv_call_share_audio_start(c, cs, C.double(gain)) != 0 }) {
		return errClosed
	}
	if !ok {
		return errors.New("no se pudo capturar el audio")
	}
	return nil
}

func (n *NativeCall) ShareAudioVolume(gain float64) {
	n.with(func(c *C.HvCall) { C.hv_call_share_audio_volume(c, C.double(gain)) })
}

func (n *NativeCall) StopShareAudio() { n.with(func(c *C.HvCall) { C.hv_call_share_audio_stop(c) }) }

func (n *NativeCall) SetDevice(kind, id string) error {
	cs := C.CString(id)
	defer C.free(unsafe.Pointer(cs))
	var r C.int
	if !n.with(func(c *C.HvCall) {
		switch kind {
		case "mic":
			r = C.hv_call_set_input(c, cs)
		case "speaker":
			r = C.hv_call_set_output(c, cs)
		case "camera":
			r = C.hv_call_set_camera(c, cs)
		}
	}) {
		return errClosed
	}
	if r == 0 {
		return errors.New("no se pudo cambiar el dispositivo")
	}
	return nil
}

func (n *NativeCall) SetRemoteVolume(v float64) {
	n.with(func(c *C.HvCall) { C.hv_call_set_remote_volume(c, C.double(v)) })
}

func (n *NativeCall) StopScreen() { n.with(func(c *C.HvCall) { C.hv_call_screen_stop(c) }) }

// VideoSink devuelve el sink de video (GObject con referencia propia) de un flujo, o nil.
func (n *NativeCall) VideoSink(stream string) unsafe.Pointer {
	cs := C.CString(stream)
	defer C.free(unsafe.Pointer(cs))
	var p unsafe.Pointer
	n.with(func(c *C.HvCall) { p = C.hv_call_video_sink(c, cs) })
	return p
}

func (n *NativeCall) Stats() string {
	var out string
	n.with(func(c *C.HvCall) {
		s := C.hv_call_stats(c)
		out = C.GoString(s)
		C.g_free(C.gpointer(unsafe.Pointer(s)))
	})
	return out
}

func (n *NativeCall) Close() {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.closed = true
	close(n.stop)
	n.mu.Unlock()
	callsMu.Lock()
	delete(calls, n.id)
	callsMu.Unlock()
	n.use.Lock() // espera a las operaciones en curso
	c := n.c
	n.c = nil
	n.use.Unlock()
	if c != nil {
		C.hv_call_free(c)
	}
}

func lookup(id *C.char) *NativeCall {
	callsMu.Lock()
	defer callsMu.Unlock()
	return calls[C.GoString(id)]
}

//export goCallCandidate
func goCallCandidate(id *C.char, mline C.int, cand *C.char) {
	if n := lookup(id); n != nil && n.ev.Candidate != nil {
		n.ev.Candidate(int(mline), C.GoString(cand))
	}
}

//export goCallState
func goCallState(id *C.char, kind *C.char, state *C.char) {
	if n := lookup(id); n != nil && n.ev.State != nil {
		k, s := C.GoString(kind), C.GoString(state)
		go n.ev.State(k, s)
	}
}

//export goCallNegotiationNeeded
func goCallNegotiationNeeded(id *C.char) {
	if n := lookup(id); n != nil && n.ev.NegotiationNeeded != nil {
		go n.ev.NegotiationNeeded()
	}
}

//export goCallStream
func goCallStream(id *C.char, stream *C.char, kind *C.char) {
	if n := lookup(id); n != nil && n.ev.Stream != nil {
		s, k := C.GoString(stream), C.GoString(kind)
		go n.ev.Stream(s, k)
	}
}

//export goCallLevel
func goCallLevel(id *C.char, who *C.char, rms C.double) {
	if n := lookup(id); n != nil && n.ev.Level != nil {
		n.ev.Level(C.GoString(who), float64(rms))
	}
}
