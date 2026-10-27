//go:build linux && !android

package media

/*
#include <stdlib.h>
#include "media.h"
*/
import "C"

import (
	"errors"
	"sync"
	"time"
	"unsafe"
)

type MicTestOptions struct {
	Mic      string  `json:"mic"`
	Speaker  string  `json:"speaker"`
	Gain     float64 `json:"gain"`     // 1.0 = 100 %
	OutGain  float64 `json:"out_gain"` // volumen del retorno
	Loopback bool    `json:"loopback"`
	Noise    bool    `json:"noise"`
	Echo     bool    `json:"echo"`
	AGC      bool    `json:"agc"`
	NoiseLvl int     `json:"noise_level"` // 0 bajo .. 3 muy alto
}

var (
	mtMu   sync.Mutex
	mtStop chan struct{}
)

func b2i(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// StartMicTest inicia la prueba y llama a onLevel (~20 veces por segundo) con rms y pico en dB.
func StartMicTest(o MicTestOptions, onLevel func(rms, peak float64)) error {
	StopMicTest()
	mic, spk := C.CString(o.Mic), C.CString(o.Speaker)
	defer C.free(unsafe.Pointer(mic))
	defer C.free(unsafe.Pointer(spk))
	if o.Gain <= 0 {
		o.Gain = 1
	}
	if o.OutGain <= 0 {
		o.OutGain = 1
	}
	if C.media_mictest_start(mic, spk, C.double(o.Gain), C.double(o.OutGain), b2i(o.Loopback), b2i(o.Noise), b2i(o.Echo), b2i(o.AGC), C.int(o.NoiseLvl)) == 0 {
		return errors.New("no se pudo abrir el micrófono")
	}
	stop := make(chan struct{})
	mtMu.Lock()
	mtStop = stop
	mtMu.Unlock()
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			var rms, peak C.double
			switch C.media_mictest_poll(100, &rms, &peak) {
			case 1:
				onLevel(float64(rms), float64(peak))
			case -1:
				return
			}
		}
	}()
	return nil
}

func StopMicTest() {
	mtMu.Lock()
	if mtStop != nil {
		close(mtStop)
		mtStop = nil
	}
	mtMu.Unlock()
	C.media_mictest_stop()
}

// Snapshot guarda un JPEG de un nodo de video de PipeWire (miniaturas de pantallas).
func Snapshot(node, width, height int, path string, timeout time.Duration) error {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	if C.media_snapshot(C.uint(node), C.int(width), C.int(height), cs, C.int(timeout.Milliseconds())) == 0 {
		return errors.New("no se pudo capturar la miniatura")
	}
	return nil
}

// VideoThumb guarda en out un cuadro JPEG de un video (de hasta maxW×maxH) y devuelve el tamaño
// del video en pantalla y su duración.
func VideoThumb(path, out string, maxW, maxH int, timeout time.Duration) (w, h int, dur time.Duration, err error) {
	cp, co := C.CString(path), C.CString(out)
	defer C.free(unsafe.Pointer(cp))
	defer C.free(unsafe.Pointer(co))
	var cw, ch C.int
	var cd C.longlong
	if C.media_video_thumb(cp, co, C.int(maxW), C.int(maxH), C.int(timeout.Milliseconds()), &cw, &ch, &cd) == 0 {
		return int(cw), int(ch), time.Duration(cd) * time.Millisecond, errors.New("no se pudo sacar un cuadro del video")
	}
	return int(cw), int(ch), time.Duration(cd) * time.Millisecond, nil
}
