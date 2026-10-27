package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/oauth"
)

type IdP struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Icon  string `json:"icon,omitempty"`
	Brand string `json:"brand,omitempty"`
}

type DiscoverResult struct {
	Homeserver   string   `json:"homeserver"`
	ServerName   string   `json:"server_name"`
	Versions     []string `json:"versions"`
	Password     bool     `json:"password"`
	SSO          []IdP    `json:"sso"`
	SSOGeneric   bool     `json:"sso_generic"`
	OAuth        bool     `json:"oauth"`
	OAuthAccount string   `json:"oauth_account_url,omitempty"`
	SlidingSync  bool     `json:"sliding_sync"`
	Token        bool     `json:"token"`
	Registration string   `json:"registration"` // open | uia | oauth | closed | unknown
}

var httpDiscover = &http.Client{Timeout: 15 * time.Second}

func isLocalHost(h string) bool {
	host := h
	if hh, _, err := net.SplitHostPort(h); err == nil {
		host = hh
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".test") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// resolveHomeserver acepta "matrix.org", "@usuario:servidor" o una URL completa.
func resolveHomeserver(ctx context.Context, input string) (string, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", errors.New("ingresá un servidor")
	}
	if strings.HasPrefix(input, "@") {
		if i := strings.Index(input, ":"); i > 0 {
			input = input[i+1:]
		}
	}
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		u, err := url.Parse(input)
		if err != nil {
			return "", "", err
		}
		u.Path = strings.TrimSuffix(u.Path, "/")
		return u.String(), u.Host, nil
	}
	serverName := strings.TrimSuffix(input, "/")
	wk, err := mautrix.DiscoverClientAPIWithClient(ctx, httpDiscover, serverName)
	if err == nil && wk != nil && wk.Homeserver.BaseURL != "" {
		return strings.TrimSuffix(wk.Homeserver.BaseURL, "/"), serverName, nil
	}
	if isLocalHost(serverName) {
		return "http://" + serverName, serverName, nil
	}
	return "https://" + serverName, serverName, nil
}

func (m *Manager) Discover(ctx context.Context, input string) (*DiscoverResult, error) {
	if pol := m.Policy(); !pol.AllowsInput(input) {
		return nil, fmt.Errorf("tu organización solo permite cuentas de: %s", strings.Join(pol.AllowedHomeservers, ", "))
	}
	hs, serverName, err := resolveHomeserver(ctx, input)
	if err != nil {
		return nil, err
	}
	cli, err := mautrix.NewClient(hs, "", "")
	if err != nil {
		return nil, err
	}
	cli.Client = m.httpClient()
	cli.Client.Timeout = 15 * time.Second
	cli.DefaultHTTPRetries = 0
	versions, err := cli.Versions(ctx)
	if err != nil {
		return nil, fmt.Errorf("no se encontró un servidor Matrix en %s", hs)
	}
	if !m.Policy().AllowsHomeserver(hs, serverName) {
		return nil, fmt.Errorf("tu organización solo permite cuentas de: %s", strings.Join(m.Policy().AllowedHomeservers, ", "))
	}
	res := &DiscoverResult{Homeserver: hs, ServerName: serverName, Registration: "unknown"}
	for _, v := range versions.Versions {
		res.Versions = append(res.Versions, v.String())
	}
	res.SlidingSync = versions.UnstableFeatures["org.matrix.simplified_msc3575"]

	// OAuth 2.0 (MSC3861 / Matrix 1.15).
	if meta, err := getOAuthMetadata(ctx, cli); err == nil && meta != nil && meta.AuthorizationEndpoint != "" {
		res.OAuth = true
		res.OAuthAccount = meta.AccountManagementURI
		res.Registration = "oauth"
	}
	flows, err := cli.GetLoginFlows(ctx)
	if err == nil {
		var raw struct {
			Flows []struct {
				Type string `json:"type"`
				IdPs []struct {
					ID    string `json:"id"`
					Name  string `json:"name"`
					Icon  string `json:"icon"`
					Brand string `json:"brand"`
				} `json:"identity_providers"`
			} `json:"flows"`
		}
		b, _ := json.Marshal(flows)
		_ = json.Unmarshal(b, &raw)
		for _, f := range raw.Flows {
			switch f.Type {
			case "m.login.password":
				res.Password = true
			case "m.login.token":
				res.Token = true
			case "m.login.sso", "m.login.cas":
				if len(f.IdPs) == 0 {
					res.SSOGeneric = true
				}
				for _, p := range f.IdPs {
					res.SSO = append(res.SSO, IdP{ID: p.ID, Name: p.Name, Icon: p.Icon, Brand: p.Brand})
				}
			}
		}
	}
	if !res.OAuth {
		res.Registration = probeRegistration(ctx, cli)
	}
	return res, nil
}

func getOAuthMetadata(ctx context.Context, cli *mautrix.Client) (*oauth.ServerMetadata, error) {
	meta, err := cli.OAuthGetServerMetadata(ctx)
	if err == nil && meta != nil && meta.AuthorizationEndpoint != "" {
		return meta, nil
	}
	// Endpoint inestable anterior a la estabilización.
	var m2 oauth.ServerMetadata
	u := cli.BuildClientURL("unstable", "org.matrix.msc2965", "auth_metadata")
	if _, err2 := cli.MakeRequest(ctx, http.MethodGet, u, nil, &m2); err2 == nil && m2.AuthorizationEndpoint != "" {
		cli.OAuthSetServerMetadata(&m2)
		return &m2, nil
	}
	return nil, err
}

func probeRegistration(ctx context.Context, cli *mautrix.Client) string {
	_, uia, err := cli.Register(ctx, &mautrix.ReqRegister[any]{})
	if uia != nil {
		return "uia"
	}
	var httpErr mautrix.HTTPError
	if errors.As(err, &httpErr) && httpErr.RespError != nil && httpErr.RespError.ErrCode == "M_FORBIDDEN" {
		return "closed"
	}
	return "unknown"
}

func deviceDisplayName() string {
	host, _ := os.Hostname()
	// En Android el nombre del equipo (p. ej. "Pixel 8") lo pasa la app; el hostname es "localhost".
	if v := os.Getenv("HILVAN_DEVICE_MODEL"); v != "" {
		host = v
	}
	osName := map[string]string{"linux": "Linux", "windows": "Windows", "darwin": "macOS", "android": "Android"}[runtime.GOOS]
	if osName == "" {
		osName = runtime.GOOS
	}
	if host != "" {
		return fmt.Sprintf("Hilvan (%s, %s)", osName, host)
	}
	return "Hilvan (" + osName + ")"
}

// userAgent identifica a Hilvan ante el servidor con el formato "Nombre/versión (equipo; sistema)",
// que los servidores (p. ej. MAS) usan para nombrar la sesión: "Hilvan on Linux" y no "Unknown device".
func userAgent(version string) string {
	osName := map[string]string{"linux": "Linux", "windows": "Windows", "darwin": "macOS", "android": "Android"}[runtime.GOOS]
	if osName == "" {
		osName = runtime.GOOS
	}
	return fmt.Sprintf("Hilvan/%s (%s; %s %s)", version, osName, osName, runtime.GOARCH)
}

func newPickleKey() string { return randomKey(32) }

func (m *Manager) httpClient() *http.Client { return newHTTPClient(m.Settings().Proxy) }

func (m *Manager) finishLogin(ctx context.Context, hs string, resp *mautrix.RespLogin, loginType string) (*AccountInfo, error) {
	if resp.WellKnown != nil && resp.WellKnown.Homeserver.BaseURL != "" {
		hs = strings.TrimSuffix(resp.WellKnown.Homeserver.BaseURL, "/")
	}
	meta := AccountMeta{UserID: resp.UserID.String(), Homeserver: hs, DeviceID: resp.DeviceID.String(), LoginType: loginType}
	sec := &accountSecrets{AccessToken: resp.AccessToken, PickleKey: newPickleKey()}
	a, err := m.addAccount(meta, sec)
	if err != nil {
		// Cerramos la sesión recién creada para no dejar dispositivos huérfanos.
		if c, e := mautrix.NewClient(hs, resp.UserID, resp.AccessToken); e == nil {
			_, _ = c.Logout(context.Background())
		}
		return nil, err
	}
	info := a.Info()
	return &info, nil
}

func (m *Manager) LoginPassword(ctx context.Context, hs, user, password string) (*AccountInfo, error) {
	cli, err := mautrix.NewClient(hs, "", "")
	if err != nil {
		return nil, err
	}
	cli.Client = m.httpClient()
	req := &mautrix.ReqLogin{Type: mautrix.AuthTypePassword, Password: password, InitialDeviceDisplayName: deviceDisplayName()}
	user = strings.TrimSpace(user)
	switch {
	case strings.Contains(user, "@") && !strings.HasPrefix(user, "@"):
		req.Identifier = mautrix.UserIdentifier{Type: mautrix.IdentifierTypeThirdParty, Medium: "email", Address: user}
	case strings.HasPrefix(user, "+"):
		req.Identifier = mautrix.UserIdentifier{Type: mautrix.IdentifierTypePhone, Country: "", Phone: user}
	default:
		req.Identifier = mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user}
	}
	resp, err := cli.Login(ctx, req)
	if err != nil {
		return nil, friendlyError(err)
	}
	return m.finishLogin(ctx, hs, resp, "password")
}

// --- SSO y OAuth (con callback local) ---

type pendingLogin struct {
	Kind       string
	Homeserver string
	State      string
	Verifier   string
	Redirect   string
	ClientID   string
	DeviceID   string
	Created    time.Time
	cli        *mautrix.Client
	once       sync.Once
}

func (m *Manager) addPending(p *pendingLogin) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	for k, v := range m.pending {
		if time.Since(v.Created) > 30*time.Minute {
			delete(m.pending, k)
		}
	}
	m.pending[p.State] = p
}

func (m *Manager) takePending(state string) *pendingLogin {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	p := m.pending[state]
	delete(m.pending, state)
	return p
}

func (m *Manager) StartSSO(ctx context.Context, hs, idp string) (map[string]string, error) {
	if m.LocalURL == "" {
		return nil, errors.New("servidor local no disponible")
	}
	state := randomKey(18)
	state = strings.NewReplacer("+", "", "/", "", "=", "").Replace(state)
	p := &pendingLogin{Kind: "sso", Homeserver: hs, State: state, Created: time.Now()}
	m.addPending(p)
	redirect := m.LocalURL + "/callback/sso?state=" + url.QueryEscape(state)
	path := "/_matrix/client/v3/login/sso/redirect"
	if idp != "" {
		path += "/" + url.PathEscape(idp)
	}
	u := strings.TrimSuffix(hs, "/") + path + "?redirectUrl=" + url.QueryEscape(redirect)
	opened := "0"
	if m.OpenURL != nil {
		m.OpenURL(u)
		opened = "1"
	}
	return map[string]string{"pending": state, "url": u, "opened": opened}, nil
}

// CompleteSSO lo llama el servidor HTTP local al recibir el loginToken.
func (m *Manager) CompleteSSO(state, loginToken string) (*AccountInfo, error) {
	p := m.takePending(state)
	if p == nil || p.Kind != "sso" {
		return nil, errors.New("solicitud de inicio de sesión desconocida o vencida")
	}
	ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
	defer cancel()
	cli, err := mautrix.NewClient(p.Homeserver, "", "")
	if err != nil {
		return nil, err
	}
	resp, err := cli.Login(ctx, &mautrix.ReqLogin{Type: mautrix.AuthTypeToken, Token: loginToken, InitialDeviceDisplayName: deviceDisplayName()})
	if err != nil {
		m.Broadcast(Push{Ev: "auth.error", D: map[string]string{"pending": state, "error": friendlyError(err).Error()}})
		return nil, err
	}
	info, err := m.finishLogin(ctx, p.Homeserver, resp, "sso")
	if err != nil {
		m.Broadcast(Push{Ev: "auth.error", D: map[string]string{"pending": state, "error": err.Error()}})
		return nil, err
	}
	m.Broadcast(Push{Ev: "auth.done", D: map[string]any{"pending": state, "account": info}})
	return info, nil
}

type oauthClientCache struct {
	ClientID string `json:"client_id"`
	Redirect string `json:"redirect"`
}

func (m *Manager) oauthClientsPath() string { return filepath.Join(m.Paths.Data, "oauth_clients.json") }

func (m *Manager) loadOAuthClients() map[string]oauthClientCache {
	out := map[string]oauthClientCache{}
	if b, err := os.ReadFile(m.oauthClientsPath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func (m *Manager) saveOAuthClient(issuer string, c oauthClientCache) {
	all := m.loadOAuthClients()
	all[issuer] = c
	b, _ := json.MarshalIndent(all, "", "  ")
	_ = os.WriteFile(m.oauthClientsPath(), b, 0o600)
}

func randomDeviceID() string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 10)
	for i := range b {
		b[i] = letters[rand.IntN(len(letters))]
	}
	return string(b)
}

func (m *Manager) StartOAuth(ctx context.Context, hs string, register bool) (map[string]string, error) {
	if m.LocalURL == "" {
		return nil, errors.New("servidor local no disponible")
	}
	cli, err := mautrix.NewClient(hs, "", "")
	if err != nil {
		return nil, err
	}
	meta, err := getOAuthMetadata(ctx, cli)
	if err != nil || meta == nil {
		return nil, errors.New("este servidor no admite OAuth 2.0")
	}
	// Redirección loopback para apps nativas (RFC 8252).
	lu, _ := url.Parse(m.LocalURL)
	redirect := "http://127.0.0.1:" + lu.Port() + "/callback/oauth"
	cached := m.loadOAuthClients()[meta.Issuer]
	clientID := cached.ClientID
	if clientID == "" || cached.Redirect != redirect {
		reg, err := cli.OAuthRegisterClient(ctx, &oauth.ClientMetadata{
			ApplicationType:         oauth.ApplicationTypeNative,
			ClientName:              "Hilvan",
			ClientURI:               "https://github.com/heelxel/hilvan",
			GrantTypes:              []oauth.GrantType{oauth.GrantTypeAuthorizationCode, oauth.GrantTypeRefreshToken},
			RedirectURIs:            []string{redirect},
			ResponseTypes:           []oauth.ResponseType{oauth.ResponseTypeCode},
			TokenEndpointAuthMethod: oauth.AuthMethodNone,
		})
		if err != nil {
			return nil, fmt.Errorf("registro del cliente OAuth: %w", err)
		}
		clientID = reg.ClientID
		m.saveOAuthClient(meta.Issuer, oauthClientCache{ClientID: clientID, Redirect: redirect})
	}
	deviceID := randomDeviceID()
	auth, err := cli.OAuthGetAuthorizationURL(ctx, oauth.GetAuthorizationURLParams{
		RedirectURI:  redirect,
		Scopes:       oauth.ScopeList{oauth.ScopeOpenID, oauth.ScopeClientAPI, oauth.ScopeDevice(id.DeviceID(deviceID))},
		ClientID:     clientID,
		ResponseMode: oauth.ResponseModeQuery,
	})
	if err != nil {
		return nil, err
	}
	authURL := auth.URL
	if register {
		authURL += "&prompt=create"
	}
	p := &pendingLogin{Kind: "oauth", Homeserver: hs, State: auth.State, Verifier: auth.CodeVerifier,
		Redirect: redirect, ClientID: clientID, DeviceID: deviceID, Created: time.Now(), cli: cli}
	m.addPending(p)
	opened := "0"
	if m.OpenURL != nil {
		m.OpenURL(authURL)
		opened = "1"
	}
	return map[string]string{"pending": auth.State, "url": authURL, "opened": opened}, nil
}

func (m *Manager) CompleteOAuth(state, code, errParam string) (*AccountInfo, error) {
	p := m.takePending(state)
	if p == nil || p.Kind != "oauth" {
		return nil, errors.New("solicitud de inicio de sesión desconocida o vencida")
	}
	fail := func(err error) (*AccountInfo, error) {
		m.Broadcast(Push{Ev: "auth.error", D: map[string]string{"pending": state, "error": err.Error()}})
		return nil, err
	}
	if errParam != "" {
		return fail(fmt.Errorf("el servidor rechazó el inicio de sesión: %s", errParam))
	}
	ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
	defer cancel()
	cli := p.cli
	tok, err := cli.OAuthExchangeToken(ctx, oauth.ExchangeTokenParams{
		CodeVerifier: p.Verifier, RedirectURI: p.Redirect, Code: code, ClientID: p.ClientID, StoreCredentials: true,
	})
	if err != nil {
		return fail(fmt.Errorf("intercambio de código: %w", err))
	}
	who, err := cli.Whoami(ctx)
	if err != nil {
		return fail(err)
	}
	deviceID := who.DeviceID.String()
	if deviceID == "" {
		deviceID = p.DeviceID
	}
	meta := AccountMeta{UserID: who.UserID.String(), Homeserver: p.Homeserver, DeviceID: deviceID, LoginType: "oauth", OAuthClientID: p.ClientID}
	sec := &accountSecrets{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, PickleKey: newPickleKey()}
	if tok.ExpiresIn.Duration > 0 {
		sec.TokenExpiry = time.Now().Add(tok.ExpiresIn.Duration).UnixMilli()
	}
	a, err := m.addAccount(meta, sec)
	if err != nil {
		return fail(err)
	}
	info := a.Info()
	m.Broadcast(Push{Ev: "auth.done", D: map[string]any{"pending": state, "account": info}})
	return &info, nil
}

// --- registro clásico (UIA) ---

type RegisterParams struct {
	Homeserver string          `json:"homeserver"`
	Username   string          `json:"username"`
	Password   string          `json:"password"`
	Auth       json.RawMessage `json:"auth,omitempty"`
}

func (m *Manager) Register(ctx context.Context, p RegisterParams) (any, error) {
	if m.Policy().DisableRegistration {
		return nil, errors.New("tu organización no permite crear cuentas desde Hilvan")
	}
	cli, err := mautrix.NewClient(p.Homeserver, "", "")
	if err != nil {
		return nil, err
	}
	req := &mautrix.ReqRegister[any]{
		Username: p.Username, Password: p.Password, InitialDeviceDisplayName: deviceDisplayName(),
	}
	if len(p.Auth) > 0 && string(p.Auth) != "null" {
		var auth map[string]any
		_ = json.Unmarshal(p.Auth, &auth)
		req.Auth = auth
	}
	resp, uia, err := cli.Register(ctx, req)
	if uia != nil {
		fallback := map[string]string{}
		for _, f := range uia.Flows {
			for _, st := range f.Stages {
				fallback[string(st)] = strings.TrimSuffix(p.Homeserver, "/") + "/_matrix/client/v3/auth/" +
					url.PathEscape(string(st)) + "/fallback/web?session=" + url.QueryEscape(uia.Session)
			}
		}
		return map[string]any{"uia": uia, "fallback": fallback}, nil
	}
	if err != nil {
		return nil, friendlyError(err)
	}
	login := &mautrix.RespLogin{AccessToken: resp.AccessToken, DeviceID: resp.DeviceID, UserID: resp.UserID}
	return m.finishLogin(ctx, p.Homeserver, login, "password")
}

func (m *Manager) Logout(ctx context.Context, userID string) error {
	a, err := m.Account(userID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if a.Meta().LoginType == "oauth" {
		_ = a.cli.OAuthRevokeToken(ctx)
	} else {
		_, _ = a.cli.Logout(ctx)
	}
	m.removeAccount(userID)
	return nil
}

// friendlyError traduce errores comunes del servidor a mensajes claros.
func friendlyError(err error) error {
	var httpErr mautrix.HTTPError
	if errors.As(err, &httpErr) && httpErr.RespError != nil {
		switch httpErr.RespError.ErrCode {
		case "M_FORBIDDEN":
			if strings.Contains(strings.ToLower(httpErr.RespError.Err), "password") || httpErr.Request != nil && strings.Contains(httpErr.Request.URL.Path, "login") {
				return errors.New("usuario o contraseña incorrectos")
			}
			return fmt.Errorf("acceso denegado: %s", httpErr.RespError.Err)
		case "M_USER_IN_USE":
			return errors.New("ese nombre de usuario ya está en uso")
		case "M_INVALID_USERNAME":
			return errors.New("nombre de usuario no válido")
		case "M_WEAK_PASSWORD":
			return errors.New("la contraseña es demasiado débil")
		case "M_LIMIT_EXCEEDED":
			return errors.New("demasiados intentos; esperá un momento")
		case "M_USER_DEACTIVATED":
			return errors.New("esta cuenta fue desactivada")
		}
		return errors.New(httpErr.RespError.Err)
	}
	return err
}
