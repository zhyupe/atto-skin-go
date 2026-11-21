package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/sessions"

	"github.com/zhyupe/atto-skin-go/internal/database/queries"
)

var errNoSession = errors.New("web session is invalid or expired")

func newCookieStore(ctx context.Context, db *sql.DB) (*sessions.CookieStore, error) {
	key, err := loadSecretKey(ctx, db, "web_cookie_key", 96)
	if err != nil {
		return nil, fmt.Errorf("initialize cookie store: %w", err)
	}
	store := sessions.NewCookieStore(key[:64], key[64:])
	store.MaxAge(86400)
	return store, nil
}

func (s *Server) saveSession(w http.ResponseWriter, r *http.Request, u user) error {
	version, err := queries.New(s.db).GetSessionVersion(r.Context(), u.ID)
	if err != nil {
		return fmt.Errorf("read cookie generation: %w", err)
	}
	cookie := sessions.NewSession(s.sessions, "atto_session")
	cookie.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		Secure:   strings.HasPrefix(s.cfg.BaseURL, "https://"),
		SameSite: http.SameSiteLaxMode,
	}
	cookie.Values = map[any]any{
		"uid":     u.ID,
		"csrf":    randomHex(),
		"expires": time.Now().Add(24 * time.Hour).Unix(),
		"version": version,
	}
	if err = cookie.Save(r, w); err != nil {
		return fmt.Errorf("save encrypted session: %w", err)
	}
	return nil
}

func (s *Server) encryptedSession(r *http.Request) (session, error) {
	cookie, err := s.sessions.Get(r, "atto_session")
	if err != nil {
		return session{}, fmt.Errorf("decode session: %w", errors.Join(errNoSession, err))
	}
	uid, uidOK := cookie.Values["uid"].(string)
	csrf, csrfOK := cookie.Values["csrf"].(string)
	expires, expiresOK := cookie.Values["expires"].(int64)
	version, versionOK := cookie.Values["version"].(int64)
	if !uidOK || !csrfOK || !expiresOK || !versionOK || uid == "" || csrf == "" || expires <= time.Now().Unix() {
		return session{}, errNoSession
	}
	current, err := queries.New(s.db).GetSessionVersion(r.Context(), uid)
	if err != nil {
		return session{}, fmt.Errorf("read cookie generation: %w", err)
	}
	if version != current {
		return session{}, errNoSession
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil {
		return session{}, fmt.Errorf("encrypted session: %w", err)
	}
	return session{User: u, CSRF: csrf}, nil
}
