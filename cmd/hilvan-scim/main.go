// hilvan-scim: puente SCIM 2.0 → Synapse. El directorio de la organización (Entra ID, Okta, Keycloak,
// authentik…) da de alta, modifica y da de baja cuentas Matrix solo, y sus grupos se reflejan como
// espacios de Matrix (la estructura de la organización dentro de Matrix).
//
//	hilvan-scim --listen 127.0.0.1:8090 --homeserver https://matrix.ejemplo.com --server-name ejemplo.com \
//	            --token-file /run/secrets/scim-token --admin-token-file /run/secrets/synapse-admin-token
package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	schemaUser    = "urn:ietf:params:scim:schemas:core:2.0:User"
	schemaGroup   = "urn:ietf:params:scim:schemas:core:2.0:Group"
	schemaList    = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaError   = "urn:ietf:params:scim:api:messages:2.0:Error"
	schemaPatch   = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	authProvider  = "scim"
	defaultPerReq = 100
)

type bridge struct {
	hs, server, token, admin string
	statePath                string
	mu                       sync.Mutex
	groups                   map[string]*groupState // id SCIM → grupo
	http                     *http.Client
}

type groupState struct {
	ID          string `json:"id"`
	Room        string `json:"room"`
	DisplayName string `json:"displayName"`
	ExternalID  string `json:"externalId,omitempty"`
	Created     int64  `json:"created"`
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8090", "dirección donde escuchar")
	hs := flag.String("homeserver", "http://localhost:8008", "URL del API de clientes de Synapse")
	server := flag.String("server-name", "", "nombre del servidor Matrix (ejemplo.com)")
	tokenFile := flag.String("token-file", "", "archivo con el token que presenta el directorio")
	adminFile := flag.String("admin-token-file", "", "archivo con el token de una cuenta administradora de Synapse")
	state := flag.String("state", "hilvan-scim.json", "archivo de estado (grupos ↔ espacios)")
	flag.Parse()
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			log.Fatalf("no se pudo leer %s: %v", p, err)
		}
		return strings.TrimSpace(string(b))
	}
	if *server == "" || *tokenFile == "" || *adminFile == "" {
		log.Fatal("faltan --server-name, --token-file o --admin-token-file")
	}
	b := &bridge{hs: strings.TrimSuffix(*hs, "/"), server: *server, token: read(*tokenFile), admin: read(*adminFile),
		statePath: *state, groups: map[string]*groupState{}, http: &http.Client{Timeout: 60 * time.Second}}
	b.load()
	log.Printf("hilvan-scim escuchando en %s para %s", *listen, *server)
	srv := &http.Server{Addr: *listen, Handler: b, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func (b *bridge) load() {
	data, err := os.ReadFile(b.statePath)
	if err != nil {
		return
	}
	var gs []*groupState
	if json.Unmarshal(data, &gs) == nil {
		for _, g := range gs {
			b.groups[g.ID] = g
		}
	}
}

func (b *bridge) saveLocked() error {
	gs := make([]*groupState, 0, len(b.groups))
	for _, g := range b.groups {
		gs = append(gs, g)
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].Created < gs[j].Created })
	data, _ := json.MarshalIndent(gs, "", "  ")
	_ = os.MkdirAll(filepath.Dir(b.statePath), 0o700)
	tmp := b.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, b.statePath)
}

// --- HTTP ---

type scimError struct {
	status int
	detail string
	typ    string
}

func (e *scimError) Error() string { return e.detail }

func fail(status int, typ, format string, a ...any) error {
	return &scimError{status: status, typ: typ, detail: fmt.Sprintf(format, a...)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var se *scimError
	if !errors.As(err, &se) {
		se = &scimError{status: 502, detail: err.Error()}
	}
	body := map[string]any{"schemas": []string{schemaError}, "status": strconv.Itoa(se.status), "detail": se.detail}
	if se.typ != "" {
		body["scimType"] = se.typ
	}
	writeJSON(w, se.status, body)
}

func (b *bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(auth), []byte(b.token)) != 1 {
		writeErr(w, fail(401, "", "token inválido"))
		return
	}
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/scim/v2"), "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var err error
	switch {
	case path == "/ServiceProviderConfig":
		writeJSON(w, 200, serviceProviderConfig())
		return
	case path == "/ResourceTypes":
		writeJSON(w, 200, resourceTypes())
		return
	case path == "/Schemas":
		writeJSON(w, 200, map[string]any{"schemas": []string{schemaList}, "totalResults": 0, "Resources": []any{}})
		return
	case parts[0] == "Users":
		err = b.users(w, r, parts[1:])
	case parts[0] == "Groups":
		err = b.groupsHandler(w, r, parts[1:])
	default:
		err = fail(404, "", "recurso desconocido")
	}
	if err != nil {
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		writeErr(w, err)
	}
}

func serviceProviderConfig() map[string]any {
	return map[string]any{
		"schemas":               []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
		"patch":                 map[string]bool{"supported": true},
		"bulk":                  map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":                map[string]any{"supported": true, "maxResults": 1000},
		"changePassword":        map[string]bool{"supported": true},
		"sort":                  map[string]bool{"supported": false},
		"etag":                  map[string]bool{"supported": false},
		"authenticationSchemes": []map[string]any{{"type": "oauthbearertoken", "name": "Token Bearer", "description": "Token fijo configurado en hilvan-scim"}},
	}
}

func resourceTypes() map[string]any {
	return map[string]any{"schemas": []string{schemaList}, "totalResults": 2, "Resources": []map[string]any{
		{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "User", "name": "User", "endpoint": "/Users", "schema": schemaUser},
		{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ResourceType"}, "id": "Group", "name": "Group", "endpoint": "/Groups", "schema": schemaGroup},
	}}
}

// --- Synapse ---

func (b *bridge) call(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, b.hs+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.admin)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return fail(502, "", "Synapse no responde: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == 404 {
		return fail(404, "", "no existe")
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return fail(resp.StatusCode, "", "Synapse: %s", nonEmpty(e.Error, resp.Status))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var localpartRe = regexp.MustCompile(`[^a-z0-9._=/+-]`)

// mxid convierte un userName del directorio (p. ej. "ana.perez@ejemplo.com") en @ana.perez:servidor.
func (b *bridge) mxid(userName string) string {
	if strings.HasPrefix(userName, "@") && strings.Contains(userName, ":") {
		return userName
	}
	lp := strings.ToLower(strings.TrimSpace(userName))
	if i := strings.IndexByte(lp, '@'); i > 0 {
		lp = lp[:i]
	}
	lp = localpartRe.ReplaceAllString(lp, "_")
	return "@" + lp + ":" + b.server
}

type synapseUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayname"`
	Deactivated bool   `json:"deactivated"`
	Admin       bool   `json:"admin"`
	CreationTS  int64  `json:"creation_ts"`
	Threepids   []struct {
		Medium  string `json:"medium"`
		Address string `json:"address"`
	} `json:"threepids"`
	ExternalIDs []struct {
		AuthProvider string `json:"auth_provider"`
		ExternalID   string `json:"external_id"`
	} `json:"external_ids"`
}

func (b *bridge) userResource(u *synapseUser) map[string]any {
	lp := strings.TrimPrefix(strings.SplitN(u.Name, ":", 2)[0], "@")
	res := map[string]any{
		"schemas":     []string{schemaUser},
		"id":          u.Name,
		"userName":    lp,
		"displayName": nonEmpty(u.DisplayName, lp),
		"name":        map[string]string{"formatted": nonEmpty(u.DisplayName, lp)},
		"active":      !u.Deactivated,
		"meta": map[string]any{"resourceType": "User", "location": "/scim/v2/Users/" + url.PathEscape(u.Name),
			"created": time.UnixMilli(normTS(u.CreationTS)).UTC().Format(time.RFC3339)},
	}
	var emails []map[string]any
	for i, t := range u.Threepids {
		if t.Medium == "email" {
			emails = append(emails, map[string]any{"value": t.Address, "primary": i == 0, "type": "work"})
		}
	}
	if emails != nil {
		res["emails"] = emails
	}
	for _, e := range u.ExternalIDs {
		if e.AuthProvider == authProvider {
			res["externalId"] = e.ExternalID
		}
	}
	return res
}

func normTS(ts int64) int64 {
	if ts < 1e12 {
		return ts * 1000
	}
	return ts
}

func (b *bridge) getUser(id string) (*synapseUser, error) {
	var u synapseUser
	if err := b.call("GET", "/_synapse/admin/v2/users/"+url.PathEscape(id), nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// scimUser es lo que manda el directorio.
type scimUser struct {
	UserName    string `json:"userName"`
	ExternalID  string `json:"externalId"`
	DisplayName string `json:"displayName"`
	Name        struct {
		Formatted  string `json:"formatted"`
		GivenName  string `json:"givenName"`
		FamilyName string `json:"familyName"`
	} `json:"name"`
	Emails []struct {
		Value   string `json:"value"`
		Primary bool   `json:"primary"`
	} `json:"emails"`
	Active   *bool  `json:"active"`
	Password string `json:"password"`
}

func (su *scimUser) displayName() string {
	if su.DisplayName != "" {
		return su.DisplayName
	}
	if su.Name.Formatted != "" {
		return su.Name.Formatted
	}
	return strings.TrimSpace(su.Name.GivenName + " " + su.Name.FamilyName)
}

func (b *bridge) applyUser(id string, su *scimUser, existing *synapseUser) error {
	body := map[string]any{}
	if dn := su.displayName(); dn != "" {
		body["displayname"] = dn
	}
	var tps []map[string]string
	for _, e := range su.Emails {
		if e.Value != "" {
			tps = append(tps, map[string]string{"medium": "email", "address": strings.ToLower(e.Value)})
		}
	}
	if tps != nil {
		body["threepids"] = tps
	}
	if su.ExternalID != "" {
		body["external_ids"] = []map[string]string{{"auth_provider": authProvider, "external_id": su.ExternalID}}
	}
	if su.Active != nil {
		if !*su.Active {
			if existing != nil && !existing.Deactivated {
				// Baja: cierra sesiones y desactiva (sin borrar mensajes, para auditoría).
				return b.call("POST", "/_synapse/admin/v1/deactivate/"+url.PathEscape(id), map[string]any{"erase": false}, nil)
			}
			body["deactivated"] = true
		} else if existing != nil && existing.Deactivated {
			body["deactivated"] = false
		}
	}
	if su.Password != "" {
		body["password"] = su.Password
		body["logout_devices"] = false
	}
	return b.call("PUT", "/_synapse/admin/v2/users/"+url.PathEscape(id), body, nil)
}

func (b *bridge) users(w http.ResponseWriter, r *http.Request, rest []string) error {
	id := ""
	if len(rest) > 0 && rest[0] != "" {
		id, _ = url.PathUnescape(rest[0])
	}
	switch {
	case r.Method == "GET" && id == "":
		return b.listUsers(w, r)
	case r.Method == "GET":
		u, err := b.getUser(id)
		if err != nil {
			return err
		}
		writeJSON(w, 200, b.userResource(u))
		return nil
	case r.Method == "POST" && id == "":
		var su scimUser
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&su); err != nil || su.UserName == "" {
			return fail(400, "invalidValue", "falta userName")
		}
		mx := b.mxid(su.UserName)
		if _, err := b.getUser(mx); err == nil {
			return fail(409, "uniqueness", "el usuario %s ya existe", mx)
		}
		if err := b.applyUser(mx, &su, nil); err != nil {
			return err
		}
		u, err := b.getUser(mx)
		if err != nil {
			return err
		}
		writeJSON(w, 201, b.userResource(u))
		return nil
	case r.Method == "PUT" && id != "":
		var su scimUser
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&su); err != nil {
			return fail(400, "invalidSyntax", "JSON inválido")
		}
		existing, err := b.getUser(id)
		if err != nil {
			return err
		}
		if su.Active == nil {
			t := true
			su.Active = &t
		}
		if err := b.applyUser(id, &su, existing); err != nil {
			return err
		}
		u, err := b.getUser(id)
		if err != nil {
			return err
		}
		writeJSON(w, 200, b.userResource(u))
		return nil
	case r.Method == "PATCH" && id != "":
		existing, err := b.getUser(id)
		if err != nil {
			return err
		}
		su, err := patchUser(r.Body)
		if err != nil {
			return err
		}
		if err := b.applyUser(id, su, existing); err != nil {
			return err
		}
		u, err := b.getUser(id)
		if err != nil {
			return err
		}
		writeJSON(w, 200, b.userResource(u))
		return nil
	case r.Method == "DELETE" && id != "":
		if err := b.call("POST", "/_synapse/admin/v1/deactivate/"+url.PathEscape(id), map[string]any{"erase": false}, nil); err != nil {
			return err
		}
		w.WriteHeader(204)
		return nil
	}
	return fail(405, "", "método no soportado")
}

type patchReq struct {
	Operations []struct {
		Op    string          `json:"op"`
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"Operations"`
}

// patchUser traduce las operaciones PATCH habituales de Entra ID / Okta a un scimUser parcial.
func patchUser(body io.Reader) (*scimUser, error) {
	var p patchReq
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&p); err != nil {
		return nil, fail(400, "invalidSyntax", "JSON inválido")
	}
	su := &scimUser{}
	for _, op := range p.Operations {
		switch strings.ToLower(op.Op) {
		case "replace", "add":
		case "remove":
			continue
		default:
			return nil, fail(400, "invalidValue", "operación %q no soportada", op.Op)
		}
		path := strings.ToLower(op.Path)
		switch {
		case path == "":
			// Valor objeto con varios atributos.
			var m map[string]json.RawMessage
			_ = json.Unmarshal(op.Value, &m)
			for k, v := range m {
				applyAttr(su, strings.ToLower(k), v)
			}
		default:
			applyAttr(su, path, op.Value)
		}
	}
	return su, nil
}

func applyAttr(su *scimUser, path string, v json.RawMessage) {
	str := func() string {
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
		return strings.Trim(string(v), `"`)
	}
	switch {
	case path == "active":
		var bv bool
		if json.Unmarshal(v, &bv) != nil {
			bv = strings.EqualFold(str(), "true")
		}
		su.Active = &bv
	case path == "displayname":
		su.DisplayName = str()
	case path == "name.formatted":
		su.Name.Formatted = str()
	case path == "name.givenname":
		su.Name.GivenName = str()
	case path == "name.familyname":
		su.Name.FamilyName = str()
	case path == "externalid":
		su.ExternalID = str()
	case path == "password":
		su.Password = str()
	case strings.HasPrefix(path, "emails"):
		if strings.Contains(path, "value") {
			su.Emails = append(su.Emails, struct {
				Value   string `json:"value"`
				Primary bool   `json:"primary"`
			}{Value: str(), Primary: true})
		} else {
			_ = json.Unmarshal(v, &su.Emails)
		}
	case path == "name":
		_ = json.Unmarshal(v, &su.Name)
	}
}

var filterRe = regexp.MustCompile(`(?i)^\s*(userName|externalId|displayName|id)\s+eq\s+"([^"]*)"\s*$`)

func (b *bridge) listUsers(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	start, _ := strconv.Atoi(q.Get("startIndex"))
	if start < 1 {
		start = 1
	}
	count, _ := strconv.Atoi(q.Get("count"))
	if count <= 0 || count > 1000 {
		count = defaultPerReq
	}
	var out []map[string]any
	total := 0
	if f := q.Get("filter"); f != "" {
		m := filterRe.FindStringSubmatch(f)
		if m == nil {
			return fail(400, "invalidFilter", "filtro no soportado: %s", f)
		}
		var u *synapseUser
		var err error
		switch strings.ToLower(m[1]) {
		case "externalid":
			var res struct {
				UserID string `json:"user_id"`
			}
			if err = b.call("GET", "/_synapse/admin/v1/auth_providers/"+authProvider+"/users/"+url.PathEscape(m[2]), nil, &res); err == nil {
				u, err = b.getUser(res.UserID)
			}
		case "id":
			u, err = b.getUser(m[2])
		default:
			u, err = b.getUser(b.mxid(m[2]))
		}
		if err == nil && u != nil {
			out = append(out, b.userResource(u))
			total = 1
		}
	} else {
		var res struct {
			Users []synapseUser `json:"users"`
			Total int           `json:"total"`
		}
		path := fmt.Sprintf("/_synapse/admin/v2/users?from=%d&limit=%d&guests=false&deactivated=true", start-1, count)
		if err := b.call("GET", path, nil, &res); err != nil {
			return err
		}
		total = res.Total
		for i := range res.Users {
			if strings.HasSuffix(res.Users[i].Name, ":"+b.server) {
				out = append(out, b.userResource(&res.Users[i]))
			}
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, 200, map[string]any{"schemas": []string{schemaList}, "totalResults": total, "startIndex": start, "itemsPerPage": len(out), "Resources": out})
	return nil
}

// --- grupos → espacios ---

func (b *bridge) groupResource(g *groupState) (map[string]any, error) {
	var res struct {
		Members []string `json:"members"`
	}
	if err := b.call("GET", "/_synapse/admin/v1/rooms/"+url.PathEscape(g.Room)+"/members", nil, &res); err != nil {
		return nil, err
	}
	admin := b.whoami()
	var members []map[string]string
	for _, m := range res.Members {
		if m != admin {
			members = append(members, map[string]string{"value": m, "display": m})
		}
	}
	if members == nil {
		members = []map[string]string{}
	}
	out := map[string]any{"schemas": []string{schemaGroup}, "id": g.ID, "displayName": g.DisplayName, "members": members,
		"meta": map[string]any{"resourceType": "Group", "location": "/scim/v2/Groups/" + g.ID, "created": time.UnixMilli(g.Created).UTC().Format(time.RFC3339)}}
	if g.ExternalID != "" {
		out["externalId"] = g.ExternalID
	}
	return out, nil
}

var whoamiCache string

func (b *bridge) whoami() string {
	if whoamiCache != "" {
		return whoamiCache
	}
	var res struct {
		UserID string `json:"user_id"`
	}
	if b.call("GET", "/_matrix/client/v3/account/whoami", nil, &res) == nil {
		whoamiCache = res.UserID
	}
	return whoamiCache
}

func (b *bridge) addMember(room, user string) error {
	return b.call("POST", "/_synapse/admin/v1/join/"+url.PathEscape(room), map[string]string{"user_id": user}, nil)
}

func (b *bridge) removeMember(room, user string) error {
	return b.call("POST", "/_matrix/client/v3/rooms/"+url.PathEscape(room)+"/kick", map[string]string{"user_id": user, "reason": "Quitado del grupo en el directorio de la organización"}, nil)
}

type scimGroup struct {
	DisplayName string `json:"displayName"`
	ExternalID  string `json:"externalId"`
	Members     []struct {
		Value string `json:"value"`
	} `json:"members"`
}

func (b *bridge) groupsHandler(w http.ResponseWriter, r *http.Request, rest []string) error {
	id := ""
	if len(rest) > 0 {
		id = rest[0]
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case r.Method == "GET" && id == "":
		f := r.URL.Query().Get("filter")
		var out []map[string]any
		for _, g := range b.groups {
			if m := regexp.MustCompile(`(?i)displayName\s+eq\s+"([^"]*)"`).FindStringSubmatch(f); m != nil && !strings.EqualFold(m[1], g.DisplayName) {
				continue
			}
			if m := regexp.MustCompile(`(?i)externalId\s+eq\s+"([^"]*)"`).FindStringSubmatch(f); m != nil && m[1] != g.ExternalID {
				continue
			}
			if res, err := b.groupResource(g); err == nil {
				out = append(out, res)
			}
		}
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, map[string]any{"schemas": []string{schemaList}, "totalResults": len(out), "startIndex": 1, "itemsPerPage": len(out), "Resources": out})
		return nil
	case r.Method == "GET":
		g := b.groups[id]
		if g == nil {
			return fail(404, "", "grupo desconocido")
		}
		res, err := b.groupResource(g)
		if err != nil {
			return err
		}
		writeJSON(w, 200, res)
		return nil
	case r.Method == "POST" && id == "":
		var sg scimGroup
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&sg); err != nil || sg.DisplayName == "" {
			return fail(400, "invalidValue", "falta displayName")
		}
		var created struct {
			RoomID string `json:"room_id"`
		}
		err := b.call("POST", "/_matrix/client/v3/createRoom", map[string]any{
			"name": sg.DisplayName, "preset": "private_chat", "creation_content": map[string]any{"type": "m.space"},
			"topic":                        "Grupo sincronizado desde el directorio de la organización",
			"power_level_content_override": map[string]any{"events_default": 100, "invite": 100},
		}, &created)
		if err != nil {
			return err
		}
		g := &groupState{ID: strconv.FormatInt(time.Now().UnixNano(), 36), Room: created.RoomID, DisplayName: sg.DisplayName, ExternalID: sg.ExternalID, Created: time.Now().UnixMilli()}
		b.groups[g.ID] = g
		if err := b.saveLocked(); err != nil {
			return err
		}
		for _, m := range sg.Members {
			if err := b.addMember(g.Room, m.Value); err != nil {
				log.Printf("grupo %s: no se pudo agregar %s: %v", g.DisplayName, m.Value, err)
			}
		}
		res, err := b.groupResource(g)
		if err != nil {
			return err
		}
		writeJSON(w, 201, res)
		return nil
	case r.Method == "PATCH" && id != "":
		g := b.groups[id]
		if g == nil {
			return fail(404, "", "grupo desconocido")
		}
		var p patchReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
			return fail(400, "invalidSyntax", "JSON inválido")
		}
		memberFilter := regexp.MustCompile(`(?i)^members\[value\s+eq\s+"([^"]+)"\]$`)
		for _, op := range p.Operations {
			path := op.Path
			switch strings.ToLower(op.Op) {
			case "add", "replace":
				if strings.EqualFold(path, "displayName") {
					var dn string
					_ = json.Unmarshal(op.Value, &dn)
					if dn != "" {
						g.DisplayName = dn
						_ = b.call("PUT", "/_matrix/client/v3/rooms/"+url.PathEscape(g.Room)+"/state/m.room.name", map[string]string{"name": dn}, nil)
					}
					continue
				}
				var ms []struct {
					Value string `json:"value"`
				}
				_ = json.Unmarshal(op.Value, &ms)
				for _, m := range ms {
					if err := b.addMember(g.Room, m.Value); err != nil {
						return err
					}
				}
			case "remove":
				if m := memberFilter.FindStringSubmatch(path); m != nil {
					if err := b.removeMember(g.Room, m[1]); err != nil {
						return err
					}
					continue
				}
				var ms []struct {
					Value string `json:"value"`
				}
				_ = json.Unmarshal(op.Value, &ms)
				for _, m := range ms {
					if err := b.removeMember(g.Room, m.Value); err != nil {
						return err
					}
				}
			}
		}
		_ = b.saveLocked()
		res, err := b.groupResource(g)
		if err != nil {
			return err
		}
		writeJSON(w, 200, res)
		return nil
	case r.Method == "DELETE" && id != "":
		g := b.groups[id]
		if g == nil {
			return fail(404, "", "grupo desconocido")
		}
		// El espacio se cierra (se expulsa a todos) pero sus mensajes quedan para auditoría.
		if err := b.call("DELETE", "/_synapse/admin/v2/rooms/"+url.PathEscape(g.Room), map[string]any{"block": false, "purge": false,
			"message": "Este grupo se eliminó en el directorio de la organización."}, nil); err != nil {
			return err
		}
		delete(b.groups, id)
		_ = b.saveLocked()
		w.WriteHeader(204)
		return nil
	}
	return fail(405, "", "método no soportado")
}
