package server

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

//go:embed web/*
var webFiles embed.FS

type Config struct {
	BaseURL        string
	DataDir        string
	Issuer         string
	ClientID       string
	ClientSecret   string
	TrustedProxies []netip.Prefix
}

func (c *Config) Validate() error {
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	for name, raw := range map[string]string{"BASE_URL": c.BaseURL, "OIDC_ISSUER": c.Issuer} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("invalid %s", name)
		}
		if name == "BASE_URL" && u.Path != "" {
			return errors.New("BASE_URL must not include a path")
		}
		local := u.Hostname() == "localhost"
		if ip, e := netip.ParseAddr(u.Hostname()); e == nil {
			local = ip.IsLoopback()
		}
		if u.Scheme != "https" && !(u.Scheme == "http" && local) {
			return fmt.Errorf("%s must use HTTPS (HTTP is allowed only on loopback)", name)
		}
	}
	if c.ClientID == "" || c.DataDir == "" {
		return errors.New("OIDC_CLIENT_ID and DATA_DIR are required")
	}
	return nil
}

type bucket struct {
	count int
	until time.Time
}
type Server struct {
	cfg        Config
	db         *sql.DB
	key        *rsa.PrivateKey
	oauth      oauth2.Config
	verifier   *oidc.IDTokenVerifier
	httpClient *http.Client
	page       *template.Template
	mu         sync.Mutex
	limits     map[string]bucket
}

func New(ctx context.Context, cfg Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	db, err := openDB(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, db: db, httpClient: &http.Client{Timeout: 15 * time.Second}, limits: map[string]bucket{}}
	fail := func(e error) (*Server, error) { db.Close(); return nil, e }
	s.key, err = loadKey(db)
	if err != nil {
		return fail(err)
	}
	s.page, err = template.ParseFS(webFiles, "web/index.html")
	if err != nil {
		return fail(err)
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, s.httpClient), cfg.Issuer)
	if err != nil {
		return fail(fmt.Errorf("OIDC discovery: %w", err))
	}
	s.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	s.oauth = oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: cfg.BaseURL + "/auth/callback", Scopes: []string{oidc.ScopeOpenID, "profile"}}
	if err = s.cleanup(); err != nil {
		return fail(err)
	}
	return s, nil
}
func (s *Server) Close() error { return s.db.Close() }
func (s *Server) Maintenance(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.cleanup(); err != nil {
				slog.Error("cleanup failed", "error", err)
			}
		}
	}
}
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", s.home)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.db.PingContext(r.Context()) != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Write([]byte("ok\n"))
	})
	m.HandleFunc("GET /static/{file}", s.static)
	m.HandleFunc("GET /auth/login", s.login)
	m.HandleFunc("GET /auth/callback", s.callback)
	m.HandleFunc("POST /auth/logout", s.withSession(s.logout))
	m.HandleFunc("POST /account/profile", s.withSession(s.rename))
	m.HandleFunc("POST /account/skin", s.withSession(s.uploadWeb))
	m.HandleFunc("POST /account/skin/delete", s.withSession(s.deleteWeb))
	m.HandleFunc("POST /account/skin/restore", s.withSession(s.restoreWeb))
	m.HandleFunc("POST /account/launcher-key", s.withSession(s.rotateKey))
	m.HandleFunc("GET /textures/{hash}", s.texture)
	m.HandleFunc("GET /api/yggdrasil", s.metadata)
	m.HandleFunc("GET /api/yggdrasil/{$}", s.metadata)
	m.HandleFunc("POST /api/yggdrasil/authserver/authenticate", s.authenticate)
	m.HandleFunc("POST /api/yggdrasil/authserver/refresh", s.refresh)
	m.HandleFunc("POST /api/yggdrasil/authserver/validate", s.validate)
	m.HandleFunc("POST /api/yggdrasil/authserver/invalidate", s.invalidate)
	m.HandleFunc("POST /api/yggdrasil/authserver/signout", s.signout)
	m.HandleFunc("POST /api/yggdrasil/sessionserver/session/minecraft/join", s.join)
	m.HandleFunc("GET /api/yggdrasil/sessionserver/session/minecraft/hasJoined", s.hasJoined)
	m.HandleFunc("GET /api/yggdrasil/sessionserver/session/minecraft/profile/{uuid}", s.profile)
	m.HandleFunc("POST /api/yggdrasil/api/profiles/minecraft", s.profiles)
	m.HandleFunc("GET /api/yggdrasil/api/users/profiles/minecraft/{name}", s.profileByName)
	m.HandleFunc("PUT /api/yggdrasil/api/user/profile/{uuid}/{kind}", s.uploadProtocol)
	m.HandleFunc("DELETE /api/yggdrasil/api/user/profile/{uuid}/{kind}", s.uploadProtocol)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// no-referrer turns the Origin of browser form POSTs into "null".
		// Keep same-origin form submissions verifiable without leaking callback URLs externally.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Authlib-Injector-API-Location", s.cfg.BaseURL+"/api/yggdrasil/")
		w.Header().Set("Cache-Control", "no-store")
		m.ServeHTTP(w, r)
	})
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func apiError(w http.ResponseWriter, status int, kind, message string) {
	jsonResponse(w, status, map[string]string{"error": kind, "errorMessage": message})
}
func internalError(w http.ResponseWriter, err error) {
	slog.Error("request failed", "error", err)
	apiError(w, 500, "InternalServerError", "An internal error occurred.")
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	if err := d.Decode(v); err != nil {
		apiError(w, 400, "IllegalArgumentException", "Invalid JSON request.")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		apiError(w, 400, "IllegalArgumentException", "Expected one JSON value.")
		return false
	}
	return true
}
func (s *Server) allow(key string, n int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.limits) >= 10000 {
		for k, v := range s.limits {
			if now.After(v.until) {
				delete(s.limits, k)
			}
		}
		if _, ok := s.limits[key]; !ok && len(s.limits) >= 10000 {
			return false
		}
	}
	b := s.limits[key]
	if now.After(b.until) {
		b = bucket{until: now.Add(time.Minute)}
	}
	b.count++
	s.limits[key] = b
	return b.count <= n
}
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	ip = ip.Unmap()
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(ip) {
			// Only a directly trusted reverse proxy may supply this single, overwritten header.
			if forwarded, e := netip.ParseAddr(r.Header.Get("X-Real-IP")); e == nil {
				return forwarded.Unmap().String()
			}
		}
	}
	return ip.String()
}
