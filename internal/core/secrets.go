package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// SecretStore guarda tokens de acceso y claves de cifrado locales.
// Preferimos el llavero del sistema (Secret Service: GNOME Keyring, KWallet);
// si no está disponible usamos un archivo con permisos 0600.
type SecretStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
	Kind() string
}

var errSecretNotFound = errors.New("secreto no encontrado")

func NewSecretStore(fallbackPath string, preferKeyring bool) SecretStore {
	if preferKeyring {
		if ss, err := newSecretService(); err == nil {
			return ss
		}
	}
	return &fileSecrets{path: fallbackPath}
}

// --- archivo (respaldo) ---

type fileSecrets struct {
	path string
	mu   sync.Mutex
}

func (f *fileSecrets) Kind() string { return "file" }

func (f *fileSecrets) load() map[string]string {
	m := map[string]string{}
	b, err := os.ReadFile(f.path)
	if err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (f *fileSecrets) save(m map[string]string) error {
	b, _ := json.Marshal(m)
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

func (f *fileSecrets) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.load()[key]
	if !ok {
		return "", errSecretNotFound
	}
	return v, nil
}

func (f *fileSecrets) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.load()
	m[key] = value
	return f.save(m)
}

func (f *fileSecrets) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.load()
	delete(m, key)
	return f.save(m)
}

// --- Secret Service (D-Bus) ---

const (
	ssDest       = "org.freedesktop.secrets"
	ssPath       = dbus.ObjectPath("/org/freedesktop/secrets")
	ssIface      = "org.freedesktop.Secret.Service"
	ssCollection = "org.freedesktop.Secret.Collection"
	ssItem       = "org.freedesktop.Secret.Item"
	ssPrompt     = "org.freedesktop.Secret.Prompt"
)

type ssSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

type secretService struct {
	conn       *dbus.Conn
	session    dbus.ObjectPath
	collection dbus.ObjectPath
	mu         sync.Mutex
}

func (s *secretService) Kind() string { return "keyring" }

func newSecretService() (*secretService, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, err
	}
	svc := conn.Object(ssDest, ssPath)
	var out dbus.Variant
	var session dbus.ObjectPath
	if err = svc.Call(ssIface+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&out, &session); err != nil {
		return nil, err
	}
	var coll dbus.ObjectPath
	if err = svc.Call(ssIface+".ReadAlias", 0, "default").Store(&coll); err != nil {
		return nil, err
	}
	if coll == "/" || coll == "" {
		return nil, errors.New("el llavero no tiene colección por defecto")
	}
	s := &secretService{conn: conn, session: session, collection: coll}
	if err = s.unlock(); err != nil {
		return nil, err
	}
	// Prueba de escritura/lectura para asegurarnos de que funciona de verdad.
	if err = s.Set("__probe__", "ok"); err != nil {
		return nil, err
	}
	if v, err := s.Get("__probe__"); err != nil || v != "ok" {
		return nil, fmt.Errorf("el llavero no devolvió el valor de prueba: %v", err)
	}
	_ = s.Delete("__probe__")
	return s, nil
}

func (s *secretService) unlock() error {
	coll := s.conn.Object(ssDest, s.collection)
	locked, err := coll.GetProperty(ssCollection + ".Locked")
	if err != nil {
		return err
	}
	if l, _ := locked.Value().(bool); !l {
		return nil
	}
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	err = s.conn.Object(ssDest, ssPath).Call(ssIface+".Unlock", 0, []dbus.ObjectPath{s.collection}).Store(&unlocked, &prompt)
	if err != nil {
		return err
	}
	if prompt != "/" {
		return s.runPrompt(prompt)
	}
	return nil
}

func (s *secretService) runPrompt(prompt dbus.ObjectPath) error {
	ch := make(chan *dbus.Signal, 4)
	s.conn.Signal(ch)
	defer s.conn.RemoveSignal(ch)
	_ = s.conn.AddMatchSignal(dbus.WithMatchObjectPath(prompt), dbus.WithMatchInterface(ssPrompt), dbus.WithMatchMember("Completed"))
	defer s.conn.RemoveMatchSignal(dbus.WithMatchObjectPath(prompt), dbus.WithMatchInterface(ssPrompt), dbus.WithMatchMember("Completed"))
	if err := s.conn.Object(ssDest, prompt).Call(ssPrompt+".Prompt", 0, "").Err; err != nil {
		return err
	}
	timeout := time.After(2 * time.Minute)
	for {
		select {
		case sig := <-ch:
			if sig.Path == prompt && sig.Name == ssPrompt+".Completed" {
				if len(sig.Body) > 0 {
					if dismissed, _ := sig.Body[0].(bool); dismissed {
						return errors.New("desbloqueo del llavero cancelado")
					}
				}
				return nil
			}
		case <-timeout:
			return errors.New("tiempo de espera agotado para el llavero")
		}
	}
}

func (s *secretService) find(key string) (dbus.ObjectPath, error) {
	var items []dbus.ObjectPath
	err := s.conn.Object(ssDest, s.collection).Call(ssCollection+".SearchItems", 0,
		map[string]string{"application": "hilvan", "key": key}).Store(&items)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", errSecretNotFound
	}
	return items[0], nil
}

func (s *secretService) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.find(key)
	if err != nil {
		return "", err
	}
	var sec ssSecret
	if err = s.conn.Object(ssDest, item).Call(ssItem+".GetSecret", 0, s.session).Store(&sec); err != nil {
		return "", err
	}
	return string(sec.Value), nil
}

func (s *secretService) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	props := map[string]dbus.Variant{
		"org.freedesktop.Secret.Item.Label":      dbus.MakeVariant("Hilvan — " + key),
		"org.freedesktop.Secret.Item.Attributes": dbus.MakeVariant(map[string]string{"application": "hilvan", "key": key}),
	}
	sec := ssSecret{Session: s.session, Parameters: []byte{}, Value: []byte(value), ContentType: "text/plain; charset=utf8"}
	var item, prompt dbus.ObjectPath
	err := s.conn.Object(ssDest, s.collection).Call(ssCollection+".CreateItem", 0, props, sec, true).Store(&item, &prompt)
	if err != nil {
		return err
	}
	if prompt != "/" {
		return s.runPrompt(prompt)
	}
	return nil
}

func (s *secretService) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.find(key)
	if err != nil {
		if errors.Is(err, errSecretNotFound) {
			return nil
		}
		return err
	}
	var prompt dbus.ObjectPath
	if err = s.conn.Object(ssDest, item).Call(ssItem+".Delete", 0).Store(&prompt); err != nil {
		return err
	}
	if prompt != "/" {
		return s.runPrompt(prompt)
	}
	return nil
}
