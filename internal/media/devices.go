//go:build linux && !android

// Package media es el motor multimedia nativo (GStreamer): dispositivos y llamadas.
package media

/*
#cgo pkg-config: gstreamer-1.0 gstreamer-sdp-1.0 gstreamer-webrtc-1.0
#include <stdlib.h>
#include <glib.h>
#include "media.h"
#include <gst/gst.h>
*/
import "C"

import (
	"encoding/json"
	"strings"
	"unsafe"
)

type Device struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Kind    string `json:"kind"` // audioinput | audiooutput | videoinput
	Default bool   `json:"default"`
}

// Devices lista micrófonos, cámaras y salidas de audio del sistema.
func Devices() ([]Device, error) {
	c := C.media_list_devices()
	defer C.g_free(C.gpointer(unsafe.Pointer(c)))
	var out []Device
	err := json.Unmarshal([]byte(C.GoString(c)), &out)
	return out, err
}

func Available() bool { return true }

// GStreamerVersion es la versión de GStreamer con la que corre Hilvan.
func GStreamerVersion() string {
	C.media_init()
	s := C.gst_version_string()
	defer C.g_free(C.gpointer(unsafe.Pointer(s)))
	return strings.TrimPrefix(C.GoString(s), "GStreamer ")
}
