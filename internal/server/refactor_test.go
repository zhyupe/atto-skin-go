package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/gorilla/sessions"
	"github.com/stretchr/testify/require"
)

func encryptedBrowser(t *testing.T, s *Server, u user) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	require.NoError(t, s.saveSession(w, httptest.NewRequest(http.MethodGet, s.cfg.BaseURL+"/", nil), u))
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	return cookies[0]
}

func readBrowser(t *testing.T, s *Server, cookie *http.Cookie) (session, error) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, s.cfg.BaseURL+"/", nil)
	r.AddCookie(cookie)
	return s.session(r)
}

func TestEncryptedSessionPersistenceAndRevocation(t *testing.T) {
	s, _ := testServer(t)
	u, key := seed(t, s, "encrypted-cookie", "Alice")
	cookie := encryptedBrowser(t, s, u)
	require.True(t, cookie.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
	require.NotContains(t, cookie.Value, u.ID)
	var count int
	require.NoError(t, s.db.QueryRow("SELECT count(*) FROM web_sessions").Scan(&count))
	require.Zero(t, count)
	before, err := readBrowser(t, s, cookie)
	require.NoError(t, err)
	reopened, err := New(t.Context(), s.cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	after, err := readBrowser(t, reopened, cookie)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(before.User, after.User))
	require.Equal(t, before.CSRF, after.CSRF)
	otherCookie := encryptedBrowser(t, s, u)
	token := loginGame(t, s, "Alice", key, "cookie-test")["accessToken"].(string)
	expect(t, formReq(s, "/auth/logout", url.Values{"csrf": {before.CSRF}}, cookie), http.StatusSeeOther)
	for _, c := range []*http.Cookie{cookie, otherCookie} {
		_, err = readBrowser(t, s, c)
		require.ErrorIs(t, err, errNoSession)
	}
	expect(
		t,
		jsonReq(s, "/api/yggdrasil/authserver/validate", map[string]string{"accessToken": token}),
		http.StatusNoContent,
	)
}

func TestEncryptedSessionRejectsTamperingAndExpiration(t *testing.T) {
	s, _ := testServer(t)
	u, _ := seed(t, s, "cookie-validation", "Alice")
	cookie := encryptedBrowser(t, s, u)
	cookie.Value = "bad" + cookie.Value
	_, err := readBrowser(t, s, cookie)
	require.ErrorIs(t, err, errNoSession)
	r := httptest.NewRequest(http.MethodGet, s.cfg.BaseURL+"/", nil)
	expired := sessions.NewSession(s.sessions, "atto_session")
	expired.Options = &sessions.Options{Path: "/", MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	expired.Values = map[any]any{
		"uid":     u.ID,
		"csrf":    "test-csrf",
		"expires": time.Now().Add(-time.Second).Unix(),
		"version": int64(0),
	}
	w := httptest.NewRecorder()
	require.NoError(t, expired.Save(r, w))
	_, err = readBrowser(t, s, w.Result().Cookies()[0])
	require.ErrorIs(t, err, errNoSession)
}

func TestCanceledRequestStopsWaitingForDatabase(t *testing.T) {
	s, _ := testServer(t)
	seed(t, s, "request-cancellation", "Alice")
	conn, err := s.db.Conn(t.Context())
	require.NoError(t, err)
	defer conn.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequestWithContext(
		ctx,
		http.MethodGet,
		s.cfg.BaseURL+"/api/yggdrasil/api/users/profiles/minecraft/Alice",
		nil,
	)
	req.Header.Set("X-Request-ID", "untrusted-id")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.Handler().ServeHTTP(w, req) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request ignored cancellation while waiting for database connection")
	}
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotEmpty(t, w.Header().Get("X-Request-ID"))
	require.NotEqual(t, "untrusted-id", w.Header().Get("X-Request-ID"))
}

func TestUpgradeVersionTwoPreservesExistingState(t *testing.T) {
	s, _ := testServer(t)
	u, key := seed(t, s, "upgrade-v2", "Alice")
	require.NoError(t, s.setSkin(t.Context(), u.ID, pngData(t, 64, 64), "slim", ""))
	cookie, _ := browser(t, s, u)
	token := loginGame(t, s, "Alice", key, "upgrade")["accessToken"].(string)
	signingKey := publicKey(s.key)
	require.NoError(t, s.Close())
	db, err := sql.Open("sqlite", s.cfg.DataDir+"/atto.db")
	require.NoError(t, err)
	_, err = db.Exec(
		"DROP TABLE goose_db_version; ALTER TABLE users DROP COLUMN session_version; PRAGMA user_version=2;",
	)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	upgraded, err := New(t.Context(), s.cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, upgraded.Close()) })
	require.Equal(t, signingKey, publicKey(upgraded.key))
	legacy, err := readBrowser(t, upgraded, cookie)
	require.NoError(t, err)
	require.Equal(t, u.ID, legacy.User.ID)
	history, _, err := upgraded.skinHistory(t.Context(), legacy.User, 0)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.True(t, history[0].Current)
	require.Equal(t, "slim", history[0].Model)
	expect(
		t,
		jsonReq(upgraded, "/api/yggdrasil/authserver/validate", map[string]string{"accessToken": token}),
		http.StatusNoContent,
	)
	// Foreign keys must remain enabled after replacing a database connection.
	upgraded.db.SetMaxIdleConns(0)
	_, err = upgraded.db.ExecContext(t.Context(), "INSERT INTO web_sessions VALUES('orphan','missing-user','csrf',0)")
	require.ErrorContains(t, err, "FOREIGN KEY")
}
