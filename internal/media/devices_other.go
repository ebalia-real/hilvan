//go:build !linux || android

package media

import (
	"errors"
	"time"
	"unsafe"
)

var errNoNative = errors.New("motor multimedia nativo no disponible")

type Device struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Default bool   `json:"default"`
}

func Devices() ([]Device, error) { return nil, nil }

func Available() bool { return false }

func GStreamerVersion() string { return "" }

type MicTestOptions struct {
	Mic      string  `json:"mic"`
	Speaker  string  `json:"speaker"`
	Gain     float64 `json:"gain"`
	OutGain  float64 `json:"out_gain"`
	Loopback bool    `json:"loopback"`
	Noise    bool    `json:"noise"`
	Echo     bool    `json:"echo"`
	AGC      bool    `json:"agc"`
	NoiseLvl int     `json:"noise_level"`
}

func StartMicTest(o MicTestOptions, onLevel func(rms, peak float64)) error {
	return errNoNative
}

func StopMicTest() {}

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

type CallEvents struct {
	Candidate         func(mline int, cand string)
	State             func(kind, state string)
	NegotiationNeeded func()
	Stream            func(stream, kind string)
	Level             func(who string, rms float64)
}

type NativeCall struct{}

func NewCall(cfg CallConfig, ev CallEvents) (*NativeCall, error) { return nil, errNoNative }
func (n *NativeCall) LinkSources(a, v int) error                 { return errNoNative }
func (n *NativeCall) Play() error                                { return errNoNative }
func (n *NativeCall) Create(offer bool) (string, error)          { return "", errNoNative }
func (n *NativeCall) SetRemote(offer bool, sdp string) error     { return errNoNative }
func (n *NativeCall) AddCandidate(mline int, cand string)        {}
func (n *NativeCall) SetMicMuted(m bool)                         {}
func (n *NativeCall) SetVideoMuted(m bool)                       {}
func (n *NativeCall) SetVolumes(mic, spk float64)                {}
func (n *NativeCall) SetBitrate(kbps int)                        {}
func (n *NativeCall) StartScreen(fd int, node uint32, fps, w, h, kbps int) (bool, error) {
	return false, errNoNative
}
func (n *NativeCall) ScreenQuality(fps, w, h, kbps int)            {}
func (n *NativeCall) ShareAudio(target string, gain float64) error { return errNoNative }
func (n *NativeCall) ShareAudioVolume(gain float64)                {}
func (n *NativeCall) StopShareAudio()                              {}
func (n *NativeCall) SetDevice(kind, id string) error              { return errNoNative }
func (n *NativeCall) SetRemoteVolume(v float64)                    {}
func (n *NativeCall) StopScreen()                                  {}
func (n *NativeCall) Stats() string                                { return "" }
func (n *NativeCall) VideoSink(stream string) unsafe.Pointer       { return nil }
func (n *NativeCall) Close()                                       {}

func Snapshot(node, width, height int, path string, timeout time.Duration) error { return errNoNative }

func VideoThumb(path, out string, maxW, maxH int, timeout time.Duration) (int, int, time.Duration, error) {
	return 0, 0, 0, errNoNative
}
