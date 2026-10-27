package core

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func TestScreens(t *testing.T) {
	if os.Getenv("HILVAN_TEST_SCREENS") == "" {
		t.Skip("HILVAN_TEST_SCREENS no definido")
	}
	res := screenSources(context.Background(), true)
	mons, _ := res["monitors"].([]ScreenSource)
	for _, m := range mons {
		n := 0
		if m.Thumb != "" {
			b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(m.Thumb, "data:image/jpeg;base64,"))
			n = len(b)
			if len(b) < 3 || b[0] != 0xFF || b[1] != 0xD8 {
				t.Errorf("%s: miniatura no es JPEG", m.Connector)
			}
		}
		t.Logf("%s %q %dx%d@%.0f primario=%v miniatura=%d bytes", m.Connector, m.Name, m.Width, m.Height, m.Refresh, m.Primary, n)
	}
	if res["direct"] != true || len(mons) == 0 {
		t.Fatalf("sin monitores: %+v", res)
	}
}
