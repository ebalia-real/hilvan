package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"hilvan/internal/media"

	"maunium.net/go/mautrix"
)

// --- registro reciente en memoria (para informes de errores; nunca sale solo) ---

type LogRing struct {
	mu    sync.Mutex
	lines []string
	max   int
	part  []byte
}

func NewLogRing(max int) *LogRing { return &LogRing{max: max} }

func (r *LogRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.part = append(r.part, p...)
	for {
		i := bytes.IndexByte(r.part, '\n')
		if i < 0 {
			break
		}
		line := string(r.part[:i])
		r.part = r.part[i+1:]
		// Nunca guardar cuerpos de pedidos (podrían tener contenido de mensajes) ni tokens.
		if strings.Contains(line, "req_body=") || strings.Contains(strings.ToLower(line), "access_token") {
			continue
		}
		r.lines = append(r.lines, line)
		if len(r.lines) > r.max {
			r.lines = r.lines[len(r.lines)-r.max:]
		}
	}
	return len(p), nil
}

func (r *LogRing) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// --- informe de problemas (análisis "soberano": solo a tu organización o a un archivo) ---

type ReportParams struct {
	Text    string `json:"text"`
	Logs    bool   `json:"logs"`
	Contact string `json:"contact"`
}

// Report envía el informe al servidor de informes de tu organización (compatible con rageshake)
// o, si no hay, lo guarda en Descargas. Hilvan no manda telemetría a terceros.
func (m *Manager) Report(ctx context.Context, p ReportParams) (map[string]any, error) {
	info := fmt.Sprintf("Hilvan %s · %s/%s · Go %s · %d cuenta(s)", m.Version, runtime.GOOS, runtime.GOARCH, runtime.Version(), len(m.Accounts()))
	logs := ""
	if p.Logs && m.LogRing != nil {
		logs = m.LogRing.String()
	}
	url := m.Policy().ReportURL
	if url == "" {
		name := uniquePath(filepath.Join(downloadsDir(), "Hilvan - informe "+time.Now().Format("2006-01-02 15-04")+".txt"))
		body := "Descripción:\n" + p.Text + "\n\nContacto: " + p.Contact + "\n" + info + "\n\nRegistro reciente:\n" + logs + "\n"
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			return nil, err
		}
		return map[string]any{"saved": name}, nil
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("text", p.Text)
	_ = w.WriteField("app", "hilvan")
	_ = w.WriteField("version", m.Version)
	_ = w.WriteField("user_agent", info)
	_ = w.WriteField("contact", p.Contact)
	if logs != "" {
		fw, _ := w.CreateFormFile("log", "hilvan.log")
		_, _ = io.WriteString(fw, logs)
	}
	_ = w.Close()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("no se pudo enviar el informe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("el servidor de informes respondió HTTP %d", resp.StatusCode)
	}
	return map[string]any{"sent": url}, nil
}

// --- SBOM (lista de componentes, CycloneDX) para cumplimiento (p. ej. Cyber Resilience Act) ---

func SBOM(version string) []byte {
	bi, _ := debug.ReadBuildInfo()
	type comp struct {
		Type       string           `json:"type"`
		Name       string           `json:"name"`
		Version    string           `json:"version"`
		Purl       string           `json:"purl,omitempty"`
		Properties []map[string]any `json:"properties,omitempty"`
	}
	var comps []comp
	if bi != nil {
		for _, d := range bi.Deps {
			mod := d
			if d.Replace != nil {
				mod = d.Replace
			}
			c := comp{Type: "library", Name: mod.Path, Version: mod.Version, Purl: "pkg:golang/" + mod.Path + "@" + mod.Version}
			if mod.Sum != "" {
				c.Properties = []map[string]any{{"name": "go:module:sum", "value": mod.Sum}}
			}
			comps = append(comps, c)
		}
	}
	if v := media.GStreamerVersion(); v != "" {
		comps = append(comps, comp{Type: "library", Name: "gstreamer", Version: v, Purl: "pkg:generic/gstreamer@" + v})
	}
	goVer := runtime.Version()
	doc := map[string]any{
		"bomFormat": "CycloneDX", "specVersion": "1.5", "version": 1,
		"metadata": map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"component": map[string]any{"type": "application", "name": "hilvan", "version": version},
			"tools":     []map[string]any{{"name": "hilvan --sbom", "version": version}},
			"properties": []map[string]any{{"name": "go:version", "value": goVer},
				{"name": "build:os", "value": runtime.GOOS + "/" + runtime.GOARCH}},
		},
		"components": comps,
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return b
}

// --- avisos de seguridad del servidor (para administradores) ---

type Advisory struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Severity string `json:"severity"`
	Patched  string `json:"patched"`
	URL      string `json:"url"`
}

// SecurityAdvisories compara la versión de Synapse del servidor con la última publicada y con los
// avisos de seguridad públicos del proyecto.
func (a *Account) SecurityAdvisories(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out := map[string]any{}
	// Versión del servidor (pública, por federación; si no, la del API de administración).
	server := ""
	for _, u := range []string{a.cli.BuildURL(mautrix.BaseURLPath{"_matrix", "federation", "v1", "version"}), a.cli.BuildURL(mautrix.BaseURLPath{"_synapse", "admin", "v1", "server_version"})} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("Authorization", "Bearer "+a.cli.AccessToken)
		resp, err := a.cli.Client.Do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if v := gjson.GetBytes(b, "server.version").Str; v != "" {
			server = v
			out["software"] = gjson.GetBytes(b, "server.name").Str
			break
		}
		if v := gjson.GetBytes(b, "server_version").Str; v != "" {
			server = v
			break
		}
	}
	if server == "" {
		return nil, errors.New("el servidor no informa su versión")
	}
	out["server_version"] = server
	cur := strings.TrimPrefix(strings.Fields(server)[0], "v")
	get := func(u string) ([]byte, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := a.m.httpClient().Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	}
	if b, err := get("https://api.github.com/repos/element-hq/synapse/releases/latest"); err == nil {
		latest := strings.TrimPrefix(gjson.GetBytes(b, "tag_name").Str, "v")
		out["latest"] = latest
		out["outdated"] = versionLess(cur, latest)
	} else {
		out["offline"] = true
		return out, nil
	}
	var advs []Advisory
	if b, err := get("https://api.github.com/repos/element-hq/synapse/security-advisories?state=published&per_page=100"); err == nil {
		for _, adv := range gjson.ParseBytes(b).Array() {
			for _, v := range adv.Get("vulnerabilities").Array() {
				if versionInRange(cur, v.Get("vulnerable_version_range").Str) {
					advs = append(advs, Advisory{ID: nonEmpty(adv.Get("cve_id").Str, adv.Get("ghsa_id").Str), Summary: adv.Get("summary").Str,
						Severity: adv.Get("severity").Str, Patched: v.Get("patched_versions").Str, URL: adv.Get("html_url").Str})
					break
				}
			}
		}
	}
	out["advisories"] = advs
	return out, nil
}

func parseVer(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, " -+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(strings.TrimLeft(p, "rc"))
		out = append(out, n)
	}
	for len(out) < 3 {
		out = append(out, 0)
	}
	return out
}

func versionLess(a, b string) bool {
	x, y := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

// versionInRange entiende rangos como "< 1.120.0" o ">= 1.100.0, < 1.120.2".
func versionInRange(v, rng string) bool {
	if strings.TrimSpace(rng) == "" {
		return false
	}
	for _, cond := range strings.Split(rng, ",") {
		cond = strings.TrimSpace(cond)
		var op, ver string
		for _, o := range []string{"<=", ">=", "<", ">", "="} {
			if strings.HasPrefix(cond, o) {
				op, ver = o, strings.TrimSpace(strings.TrimPrefix(cond, o))
				break
			}
		}
		if op == "" {
			op, ver = "=", cond
		}
		less, eq := versionLess(v, ver), !versionLess(v, ver) && !versionLess(ver, v)
		ok := map[string]bool{"<": less, "<=": less || eq, ">": !less && !eq, ">=": !less, "=": eq}[op]
		if !ok {
			return false
		}
	}
	return true
}
