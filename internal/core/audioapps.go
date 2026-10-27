package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// AudioApp es una aplicación que está reproduciendo audio (para compartir solo su sonido).
type AudioApp struct {
	ID    string `json:"id"` // object.serial de PipeWire
	Name  string `json:"name"`
	Media string `json:"media,omitempty"`
	Icon  string `json:"icon,omitempty"`
}

// audioApps lista los flujos de reproducción de PipeWire, sin los de Hilvan.
func audioApps(ctx context.Context) ([]AudioApp, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pw-dump").Output()
	if err != nil {
		return []AudioApp{}, errors.New("PipeWire no está disponible")
	}
	var objs []struct {
		Type string `json:"type"`
		Info struct {
			Props map[string]any `json:"props"`
		} `json:"info"`
	}
	if err := json.Unmarshal(out, &objs); err != nil {
		return []AudioApp{}, err
	}
	str := func(m map[string]any, k string) string {
		switch v := m[k].(type) {
		case string:
			return v
		case float64:
			return strconv.FormatInt(int64(v), 10)
		}
		return ""
	}
	self := strconv.Itoa(os.Getpid())
	apps := []AudioApp{}
	for _, o := range objs {
		p := o.Info.Props
		if o.Type != "PipeWire:Interface:Node" || str(p, "media.class") != "Stream/Output/Audio" {
			continue
		}
		if str(p, "application.process.id") == self || strings.Contains(strings.ToLower(str(p, "application.name")), "hilvan") {
			continue
		}
		name := str(p, "application.name")
		if name == "" {
			name = str(p, "node.name")
		}
		apps = append(apps, AudioApp{ID: str(p, "object.serial"), Name: name, Media: str(p, "media.name"), Icon: str(p, "application.icon-name")})
	}
	return apps, nil
}
