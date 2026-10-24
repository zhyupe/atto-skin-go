package server

import (
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

type router struct {
	mux *http.ServeMux
	api huma.API
}

func newRouter() *router {
	mux := http.NewServeMux()
	config := huma.DefaultConfig("Atto Skin", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	return &router{mux: mux, api: humago.New(mux, config)}
}

func (m *router) HandleFunc(pattern string, handler http.HandlerFunc) {
	method, path, _ := strings.Cut(pattern, " ")
	// Yggdrasil requires its own response envelopes and status codes.
	m.api.Adapter().Handle(&huma.Operation{Method: method, Path: path}, func(ctx huma.Context) {
		r, w := humago.Unwrap(ctx)
		handler(w, r)
	})
}

func (m *router) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.mux.ServeHTTP(w, r) }
