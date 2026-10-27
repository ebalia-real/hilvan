package core

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Actualizador: busca versiones nuevas en GitHub, baja la de esta plataforma y la instala.
//
// Seguridad: cada versión publica SHA256SUMS (la huella de cada archivo) firmado con una clave
// Ed25519 que solo tiene quien publica (no está en GitHub). Hilvan verifica la firma con la clave
// pública de abajo y la huella del archivo antes de instalar nada.

// updateAPI es la última versión publicada; updatePublicKey verifica las versiones (la clave
// privada vive solo en la computadora de quien publica).
var (
	updateAPI       = "https://api.github.com/repos/ebalia-real/hilvan/releases/latest"
	updatePublicKey = "AFLpj7CpH+ZhXM99dNbP2DS7/GAXq3cqIF9VbHJl8LA="
)

// Packaging dice cómo se instaló Hilvan (lo fija cada paquete al compilar): según eso se actualiza.
// tar | deb | flatpak | nix | windows | macos | android | dev
var Packaging = "dev"

type UpdateInfo struct {
	State    string  `json:"state"` // uptodate | available | downloading | ready | installing | error | manual
	Current  string  `json:"current"`
	Version  string  `json:"version,omitempty"`
	Notes    string  `json:"notes,omitempty"`
	URL      string  `json:"url,omitempty"` // página de la versión
	Asset    string  `json:"asset,omitempty"`
	Size     int64   `json:"size,omitempty"`
	Progress float64 `json:"progress,omitempty"`
	Manual   string  `json:"manual,omitempty"` // cómo actualizar si no se puede solo (Nix…)
	Error    string  `json:"error,omitempty"`
	Checked  int64   `json:"checked,omitempty"`
}

type updater struct {
	mu      sync.Mutex
	info    UpdateInfo
	release *ghRelease
	busy    bool
}

type ghRelease struct {
	Tag     string `json:"tag_name"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	Pre     bool   `json:"prerelease"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// assetName es el archivo de la versión que corresponde a esta instalación.
func assetName(version string) string {
	arch := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[runtime.GOARCH]
	switch Packaging {
	case "windows":
		return fmt.Sprintf("hilvan-%s-windows-%s.zip", version, arch)
	case "macos":
		return fmt.Sprintf("hilvan-%s-macos-%s.zip", version, arch)
	case "tar":
		return fmt.Sprintf("hilvan-%s-linux-%s.tar.gz", version, arch)
	case "deb":
		return fmt.Sprintf("hilvan_%s_%s.deb", version, map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH])
	case "flatpak":
		return fmt.Sprintf("hilvan-%s-%s.flatpak", version, arch)
	case "android":
		return fmt.Sprintf("hilvan-%s.apk", version)
	}
	return ""
}

// parseVersion: "v1.2.3" o "1.2.3-android" → [1 2 3].
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func newerVersion(latest, current string) bool {
	a, ok1 := parseVersion(latest)
	b, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func (m *Manager) pushUpdate() {
	m.upd.mu.Lock()
	info := m.upd.info
	m.upd.mu.Unlock()
	m.Broadcast(Push{Ev: "update", D: info})
}

func (m *Manager) setUpdate(f func(*UpdateInfo)) {
	m.upd.mu.Lock()
	f(&m.upd.info)
	m.upd.mu.Unlock()
	m.pushUpdate()
}

// UpdateStatus devuelve el último estado conocido.
func (m *Manager) UpdateStatus() UpdateInfo {
	m.upd.mu.Lock()
	defer m.upd.mu.Unlock()
	info := m.upd.info
	info.Current = m.Version
	return info
}

// CheckUpdate consulta la última versión publicada.
func (m *Manager) CheckUpdate(ctx context.Context) (UpdateInfo, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, updateAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent(m.Version))
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return m.UpdateStatus(), err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		m.setUpdate(func(i *UpdateInfo) { *i = UpdateInfo{State: "uptodate", Checked: time.Now().UnixMilli()} })
		return m.UpdateStatus(), nil
	}
	if resp.StatusCode != http.StatusOK {
		return m.UpdateStatus(), fmt.Errorf("GitHub respondió %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return m.UpdateStatus(), err
	}
	m.upd.mu.Lock()
	busy := m.upd.busy
	m.upd.mu.Unlock()
	if busy {
		return m.UpdateStatus(), nil
	}
	latest := strings.TrimPrefix(rel.Tag, "v")
	info := UpdateInfo{State: "uptodate", Version: latest, Notes: rel.Body, URL: rel.HTMLURL, Checked: time.Now().UnixMilli()}
	if !rel.Draft && !rel.Pre && newerVersion(latest, m.Version) {
		info.State = "available"
		name := assetName(latest)
		for _, a := range rel.Assets {
			if a.Name == name {
				info.Asset, info.Size = a.Name, a.Size
			}
		}
		switch {
		case Packaging == "nix":
			info.State, info.Manual = "manual", "Actualizá el flake de Hilvan y volvé a compilar el sistema (nix flake update; nixos-rebuild switch)."
		case info.Asset == "":
			info.State, info.Manual = "manual", "Bajá la versión nueva desde la página de descargas."
		}
	}
	m.upd.mu.Lock()
	m.upd.info, m.upd.release = info, &rel
	m.upd.mu.Unlock()
	m.pushUpdate()
	return m.UpdateStatus(), nil
}

// updateLoop revisa al arrancar (con un minuto de margen) y cada 12 horas.
func (m *Manager) updateLoop() {
	if Packaging == "dev" {
		return
	}
	select {
	case <-m.ctx.Done():
		return
	case <-time.After(time.Minute):
	}
	for {
		if m.Settings().UpdateCheck {
			ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
			_, err := m.CheckUpdate(ctx)
			cancel()
			if err != nil {
				m.Log.Debug().Err(err).Msg("No se pudo buscar actualizaciones")
			}
		}
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(12 * time.Hour):
		}
	}
}

func (m *Manager) download(ctx context.Context, url, dest string, onProgress func(done, total int64)) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", userAgent(m.Version))
	c := m.httpClient()
	c.Timeout = 0
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("descarga: %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	var done int64
	buf := make([]byte, 256<<10)
	last := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			if onProgress != nil && time.Since(last) > 300*time.Millisecond {
				onProgress(done, resp.ContentLength)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return f.Sync()
}

// verifiedSums baja SHA256SUMS y su firma, y la verifica con la clave pública incluida.
func (m *Manager) verifiedSums(ctx context.Context, rel *ghRelease, dir string) (map[string]string, error) {
	var sumsURL, sigURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case "SHA256SUMS":
			sumsURL = a.URL
		case "SHA256SUMS.sig":
			sigURL = a.URL
		}
	}
	if sumsURL == "" || sigURL == "" {
		return nil, errors.New("la versión no está firmada: no se instala")
	}
	sp, gp := filepath.Join(dir, "SHA256SUMS"), filepath.Join(dir, "SHA256SUMS.sig")
	if err := m.download(ctx, sumsURL, sp, nil); err != nil {
		return nil, err
	}
	if err := m.download(ctx, sigURL, gp, nil); err != nil {
		return nil, err
	}
	sums, _ := os.ReadFile(sp)
	sigB, _ := os.ReadFile(gp)
	pub, _ := base64.StdEncoding.DecodeString(updatePublicKey)
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigB)))
	if err != nil || len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, sums, sig) {
		return nil, errors.New("la firma de la versión NO es válida: no se instala")
	}
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return out, nil
}

// InstallUpdate baja, verifica e instala la versión nueva (en segundo plano; avisa con "update").
func (m *Manager) InstallUpdate() error {
	m.upd.mu.Lock()
	rel, info := m.upd.release, m.upd.info
	if m.upd.busy {
		m.upd.mu.Unlock()
		return errors.New("ya se está actualizando")
	}
	if rel == nil || info.State != "available" || info.Asset == "" {
		m.upd.mu.Unlock()
		return errors.New("no hay una actualización para instalar")
	}
	m.upd.busy = true
	m.upd.mu.Unlock()
	go func() {
		err := m.installUpdate(rel, info)
		m.upd.mu.Lock()
		m.upd.busy = false
		m.upd.mu.Unlock()
		if err != nil {
			m.Log.Warn().Err(err).Msg("No se pudo actualizar")
			m.setUpdate(func(i *UpdateInfo) { i.State, i.Error = "error", err.Error() })
		}
	}()
	return nil
}

func (m *Manager) installUpdate(rel *ghRelease, info UpdateInfo) error {
	ctx := m.ctx
	dir := filepath.Join(m.Paths.Cache, "actualizacion")
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sums, err := m.verifiedSums(ctx, rel, dir)
	if err != nil {
		return err
	}
	want := sums[info.Asset]
	if want == "" {
		return errors.New("el archivo no figura en la lista firmada")
	}
	var url string
	for _, a := range rel.Assets {
		if a.Name == info.Asset {
			url = a.URL
		}
	}
	file := filepath.Join(dir, info.Asset)
	m.setUpdate(func(i *UpdateInfo) { i.State, i.Progress, i.Error = "downloading", 0, "" })
	if err := m.download(ctx, url, file, func(done, total int64) {
		if total > 0 {
			m.setUpdate(func(i *UpdateInfo) { i.Progress = float64(done) / float64(total) })
		}
	}); err != nil {
		return err
	}
	sum, err := fileSHA256(file)
	if err != nil {
		return err
	}
	if hex.EncodeToString(sum) != want {
		return errors.New("el archivo descargado no coincide con la versión firmada: no se instala")
	}
	m.setUpdate(func(i *UpdateInfo) { i.State, i.Progress = "installing", 1 })
	return m.applyUpdate(file, dir)
}

// applyUpdate instala el archivo ya verificado según el tipo de instalación.
func (m *Manager) applyUpdate(file, dir string) error {
	switch Packaging {
	case "android":
		if m.InstallPackage == nil {
			return errors.New("no se puede instalar en este sistema")
		}
		return m.InstallPackage(file)
	case "deb":
		// El instalador del sistema pide la contraseña y resuelve dependencias.
		if pk, err := exec.LookPath("pkexec"); err == nil {
			if out, err := exec.Command(pk, "apt-get", "install", "-y", file).CombinedOutput(); err == nil {
				return m.restartAfterUpdate("")
			} else {
				m.Log.Warn().Str("salida", string(out)).Msg("apt-get no pudo instalar")
			}
		}
		return exec.Command("xdg-open", file).Start()
	case "flatpak":
		out, err := exec.Command("flatpak-spawn", "--host", "flatpak", "install", "--user", "-y", "--noninteractive", "--bundle", file).CombinedOutput()
		if err != nil {
			return fmt.Errorf("flatpak: %v %s", err, strings.TrimSpace(string(out)))
		}
		return m.restartAfterUpdate("")
	case "windows", "tar":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		exe, _ = filepath.EvalSymlinks(exe)
		want := "hilvan"
		if Packaging == "windows" {
			want = "hilvan.exe"
		}
		newBin := filepath.Join(dir, "nuevo-"+want)
		if err := extractOne(file, want, newBin); err != nil {
			return err
		}
		// Un ejecutable abierto se puede renombrar (también en Windows): el viejo queda al lado y
		// se borra en el próximo arranque.
		old := exe + ".viejo"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return fmt.Errorf("no se pudo reemplazar Hilvan (¿falta permiso de escritura en %s?): %w", filepath.Dir(exe), err)
		}
		if err := copyFileMode(newBin, exe, 0o755); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return m.restartAfterUpdate(exe)
	case "macos":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		app := exe
		for app != "/" && !strings.HasSuffix(app, ".app") {
			app = filepath.Dir(app)
		}
		if !strings.HasSuffix(app, ".app") {
			return errors.New("no se encontró Hilvan.app")
		}
		tmp := filepath.Join(dir, "app")
		if err := extractZip(file, tmp); err != nil {
			return err
		}
		newApp := filepath.Join(tmp, "Hilvan.app")
		old := app + ".viejo"
		_ = os.RemoveAll(old)
		if err := os.Rename(app, old); err != nil {
			return fmt.Errorf("no se pudo reemplazar Hilvan.app: %w", err)
		}
		if err := os.Rename(newApp, app); err != nil {
			_ = os.Rename(old, app)
			return err
		}
		go func() { time.Sleep(3 * time.Second); _ = os.RemoveAll(old) }()
		return m.restartAfterUpdate(app)
	}
	return errors.New("esta instalación se actualiza desde fuera de Hilvan")
}

// restartAfterUpdate vuelve a abrir Hilvan (la versión nueva) y cierra esta.
func (m *Manager) restartAfterUpdate(target string) error {
	m.setUpdate(func(i *UpdateInfo) { i.State = "ready" })
	go func() {
		time.Sleep(800 * time.Millisecond)
		var cmd *exec.Cmd
		switch {
		case Packaging == "macos":
			cmd = exec.Command("open", "-n", target)
		case Packaging == "flatpak":
			cmd = exec.Command("flatpak-spawn", "--host", "flatpak", "run", "io.github.heelxel.Hilvan")
		case target != "":
			cmd = exec.Command(target)
		default:
			if exe, err := os.Executable(); err == nil {
				cmd = exec.Command(exe)
			}
		}
		if m.Quit != nil {
			m.Quit()
		}
		// La nueva espera a que esta suelte la base de datos.
		time.Sleep(1500 * time.Millisecond)
		if cmd != nil {
			_ = cmd.Start()
		}
	}()
	return nil
}

// CleanOldBinary borra el ejecutable anterior que dejó una actualización.
func CleanOldBinary() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".viejo")
	}
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// extractOne saca un archivo (por nombre base) de un .zip o .tar.gz.
func extractOne(archive, name, dest string) error {
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if filepath.Base(f.Name) == name && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return err
				}
				defer rc.Close()
				out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
				if err != nil {
					return err
				}
				defer out.Close()
				_, err = io.Copy(out, io.LimitReader(rc, 1<<30))
				return err
			}
		}
		return fmt.Errorf("%s no está en el paquete", name)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s no está en el paquete", name)
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return err
			}
			defer out.Close()
			_, err = io.Copy(out, io.LimitReader(tr, 1<<30))
			return err
		}
	}
}

// extractZip extrae un .zip completo (por ejemplo Hilvan.app) sin salirse de dest.
func extractZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	root, _ := filepath.Abs(dest)
	for _, f := range zr.File {
		p := filepath.Join(root, f.Name)
		if !strings.HasPrefix(p, root+string(os.PathSeparator)) {
			return errors.New("paquete inválido")
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if f.Mode()&os.ModeSymlink != 0 {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			target, _ := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			t := string(target)
			resolved := filepath.Clean(filepath.Join(filepath.Dir(p), t))
			if filepath.IsAbs(t) || !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
				return errors.New("paquete inválido")
			}
			if err := os.Symlink(t, p); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 2<<30))
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
