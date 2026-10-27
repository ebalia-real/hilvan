package core

import (
	"bytes"
	"image/jpeg"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestVideoInfo(t *testing.T) {
	gst, err := exec.LookPath("gst-launch-1.0")
	if err != nil {
		t.Skip("sin gst-launch-1.0")
	}
	path := filepath.Join(t.TempDir(), "prueba.webm")
	// 3 s de 1280×720 con píxeles no cuadrados (se ve como 1440×720).
	cmd := exec.Command(gst, "-q", "videotestsrc", "num-buffers=90", "!",
		"video/x-raw,width=1280,height=720,framerate=30/1,pixel-aspect-ratio=9/8", "!", "vp8enc", "deadline=1", "!", "webmmux", "!", "filesink", "location="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("no se pudo generar el video: %v %s", err, out)
	}
	start := time.Now()
	thumb, w, h, dur := videoInfo(path)
	t.Logf("%dx%d %v en %v, miniatura %d bytes", w, h, dur, time.Since(start), len(thumb))
	if w != 1440 || h != 720 {
		t.Fatalf("tamaño %dx%d, quería 1440x720", w, h)
	}
	if dur < 2900*time.Millisecond || dur > 3100*time.Millisecond {
		t.Fatalf("duración %v", dur)
	}
	img, err := jpeg.Decode(bytes.NewReader(thumb))
	if err != nil {
		t.Fatalf("miniatura inválida: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 800 || b.Dy() != 400 {
		t.Fatalf("miniatura %v, quería 800x400", b)
	}
	if th, _, _, _ := videoInfo(filepath.Join(t.TempDir(), "no-existe.mp4")); th != nil {
		t.Fatal("miniatura de un archivo inexistente")
	}
}
