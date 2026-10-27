package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"maunium.net/go/mautrix"
)

// Panel de administración para servidores Synapse (API /_synapse/admin).

func (a *Account) IsServerAdmin(ctx context.Context) bool {
	var resp struct {
		Admin bool `json:"admin"`
	}
	u := a.cli.BuildURL(mautrix.BaseURLPath{"_synapse", "admin", "v1", "users", a.UserID(), "admin"})
	if _, err := a.cli.MakeRequest(ctx, http.MethodGet, u, nil, &resp); err != nil {
		return false
	}
	return resp.Admin
}

// AdminRequest hace una petición a la API de administración (solo rutas /_synapse/admin/).
func (a *Account) AdminRequest(ctx context.Context, method, path string, query map[string]string, body json.RawMessage) (json.RawMessage, error) {
	method = strings.ToUpper(method)
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete:
	default:
		return nil, errors.New("método no permitido")
	}
	clean := "/" + strings.TrimLeft(path, "/")
	if !strings.HasPrefix(clean, "/_synapse/admin/") || strings.Contains(clean, "..") {
		return nil, errors.New("ruta no permitida")
	}
	u, err := url.Parse(strings.TrimSuffix(a.cli.HomeserverURL.String(), "/") + clean)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	var reqBody any
	if len(body) > 0 && string(body) != "null" {
		reqBody = json.RawMessage(body)
	}
	var out json.RawMessage
	_, err = a.cli.MakeRequest(ctx, method, u.String(), reqBody, &out)
	if err != nil {
		return nil, friendlyError(err)
	}
	if out == nil {
		out = json.RawMessage("{}")
	}
	return out, nil
}
