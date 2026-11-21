package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

const launcherKeyViewPath = "/account/launcher-key/view"

func revealedLauncherKey(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	expect(t, w, http.StatusOK)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	match := regexp.MustCompile(`id="launcher-key"[^>]*value="([a-f0-9]{64})"`).FindStringSubmatch(w.Body.String())
	require.Len(t, match, 2)
	return match[1]
}

func TestLauncherKeyCanBeViewedWithoutReset(t *testing.T) {
	s, _ := testServer(t)
	u, err := s.ensureUser(t.Context(), s.cfg.Issuer, "new-launcher")
	require.NoError(t, err)
	cookie, csrf := browser(t, s, u)
	form := url.Values{"csrf": {csrf}}
	expect(t, formReq(s, launcherKeyViewPath, form, nil), http.StatusUnauthorized)
	expect(t, formReq(s, launcherKeyViewPath, nil, cookie), http.StatusForbidden)

	key := revealedLauncherKey(t, formReq(s, launcherKeyViewPath, form, cookie))
	raw := loginGame(t, s, u.Name, key, "launcher-view")["accessToken"].(string)
	require.NotContains(t, request(s, "GET", "/", nil, cookie, nil).Body.String(), key)
	require.Equal(t, key, revealedLauncherKey(t, formReq(s, launcherKeyViewPath, form, cookie)))

	reopened, err := New(t.Context(), s.cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	require.Equal(t, key, revealedLauncherKey(t, formReq(reopened, launcherKeyViewPath, form, cookie)))
	token := map[string]string{"accessToken": raw}
	expect(t, jsonReq(reopened, "/api/yggdrasil/authserver/validate", token), http.StatusNoContent)

	reset := formReq(reopened, "/account/launcher-key", form, cookie)
	expect(t, reset, http.StatusSeeOther)
	nextKey := revealedLauncherKey(t, formReq(reopened, launcherKeyViewPath, form, cookie))
	require.NotEqual(t, key, nextKey)
	expect(t, jsonReq(reopened, "/api/yggdrasil/authserver/validate", token), http.StatusForbidden)
	expect(t, jsonReq(reopened, "/api/yggdrasil/authserver/authenticate", map[string]string{
		"username": u.Name,
		"password": key,
	}), http.StatusForbidden)
	loginGame(t, reopened, u.Name, nextKey, "launcher-reset")
}
