package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"image"
	"image/color"
	"image/png"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeOIDC struct {
	server           *httptest.Server
	key              *rsa.PrivateKey
	mu               sync.Mutex
	nonce, challenge string
	overrides        map[string]any
	badSignature     bool
	exchanges        int
}

func newProvider(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeOIDC{key: key, overrides: map[string]any{}}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			jsonResponse(
				w,
				200,
				map[string]any{
					"issuer":                                p.server.URL,
					"authorization_endpoint":                p.server.URL + "/authorize",
					"token_endpoint":                        p.server.URL + "/token",
					"jwks_uri":                              p.server.URL + "/keys",
					"response_types_supported":              []string{"code"},
					"subject_types_supported":               []string{"public"},
					"id_token_signing_alg_values_supported": []string{"RS256"},
					"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
					"code_challenge_methods_supported":      []string{"S256"},
				},
			)
		case "/keys":
			jsonResponse(
				w,
				200,
				map[string]any{
					"keys": []any{
						map[string]any{
							"kty": "RSA",
							"use": "sig",
							"alg": "RS256",
							"kid": "test",
							"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
							"e":   "AQAB",
						},
					},
				},
			)
		case "/token":
			p.mu.Lock()
			defer p.mu.Unlock()
			p.exchanges++
			_ = r.ParseForm()
			id, secret, ok := r.BasicAuth()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || id != "atto" || secret != "secret" || r.Form.Get("grant_type") != "authorization_code" ||
				r.Form.Get("code") != "valid-code" ||
				r.Form.Get("redirect_uri") != "http://localhost:8080/auth/callback" ||
				base64.RawURLEncoding.EncodeToString(hash[:]) != p.challenge {
				jsonResponse(w, 400, map[string]string{"error": "invalid_grant"})
				return
			}
			claims := map[string]any{
				"iss":   p.server.URL,
				"sub":   "subject-1",
				"aud":   "atto",
				"exp":   time.Now().Add(time.Hour).Unix(),
				"iat":   time.Now().Unix(),
				"nonce": p.nonce,
			}
			maps.Copy(claims, p.overrides)
			header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test"})
			body, _ := json.Marshal(claims)
			unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
			h := sha256.Sum256([]byte(unsigned))
			sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
			if err != nil {
				t.Error(err)
				http.Error(w, "sign", http.StatusInternalServerError)
				return
			}
			if p.badSignature {
				sig[0] ^= 255
			}
			jsonResponse(
				w,
				200,
				map[string]any{
					"access_token": "provider-access",
					"token_type":   "Bearer",
					"expires_in":   3600,
					"id_token":     unsigned + "." + base64.RawURLEncoding.EncodeToString(sig),
				},
			)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}
func testServer(t *testing.T) (*Server, *fakeOIDC) {
	t.Helper()
	p := newProvider(t)
	s, err := New(
		context.Background(),
		Config{
			BaseURL:      "http://localhost:8080",
			DataDir:      t.TempDir(),
			Issuer:       p.server.URL,
			ClientID:     "atto",
			ClientSecret: "secret",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}

func request(
	s *Server,
	method, path string,
	body io.Reader,
	cookie *http.Cookie,
	headers map[string]string,
) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:8080"+path, body)
	r.RemoteAddr = "192.0.2.1:1234"
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func jsonReq(s *Server, path string, v any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(v)
	return request(s, "POST", path, bytes.NewReader(b), nil, map[string]string{"Content-Type": "application/json"})
}
func expect(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
	}
}
func unmarshal[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func seed(t *testing.T, s *Server, subject, name string) (user, string) {
	t.Helper()
	u, err := s.ensureUser(t.Context(), s.cfg.Issuer, subject)
	if err != nil {
		t.Fatal(err)
	}
	key := randomHex()
	if _, err = s.db.Exec("UPDATE users SET name=?,secret_hash=? WHERE id=?", name, digest(key), u.ID); err != nil {
		t.Fatal(err)
	}
	u, err = s.userByID(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return u, key
}
func browser(t *testing.T, s *Server, u user) (*http.Cookie, string) {
	t.Helper()
	raw, csrf := randomHex(), randomHex()
	_, err := s.db.Exec(
		"INSERT INTO web_sessions VALUES(?,?,?,?)",
		digest(raw),
		u.ID,
		csrf,
		time.Now().Add(time.Hour).Unix(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "atto_session", Value: raw}, csrf
}
func formReq(s *Server, path string, v url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	return request(
		s,
		"POST",
		path,
		strings.NewReader(v.Encode()),
		cookie,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": s.cfg.BaseURL},
	)
}
func loginGame(t *testing.T, s *Server, name, key, client string) map[string]any {
	t.Helper()
	w := jsonReq(
		s,
		"/api/yggdrasil/authserver/authenticate",
		map[string]any{
			"username":    name,
			"password":    key,
			"clientToken": client,
			"requestUser": true,
			"agent":       map[string]any{"name": "Minecraft", "version": 1},
		},
	)
	expect(t, w, 200)
	return unmarshal[map[string]any](t, w)
}
func beginOIDC(t *testing.T, s *Server, p *fakeOIDC) (*http.Cookie, string) {
	t.Helper()
	w := request(s, "GET", "/auth/login", nil, nil, nil)
	expect(t, w, 302)
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := location.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatal("missing PKCE/nonce/state")
	}
	p.mu.Lock()
	p.nonce = q.Get("nonce")
	p.challenge = q.Get("code_challenge")
	p.mu.Unlock()
	return w.Result().Cookies()[0], q.Get("state")
}

func TestOIDCLoginAndIdentity(t *testing.T) {
	s, p := testServer(t)
	var firstID string
	for i := range 2 {
		cookie, state := beginOIDC(t, s, p)
		w := request(s, "GET", "/auth/callback?code=valid-code&state="+state, nil, cookie, nil)
		expect(t, w, 303)
		var sess *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == "atto_session" {
				sess = c
			}
		}
		if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode {
			t.Fatal("missing secure session attributes")
		}
		home := request(s, "GET", "/", nil, sess, nil)
		expect(t, home, 200)
		if !strings.Contains(home.Body.String(), "连接启动器") {
			t.Fatal("dashboard not rendered")
		}
		var id string
		if err := s.db.QueryRow("SELECT id FROM users").Scan(&id); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstID = id
		} else if id != firstID {
			t.Fatal("OIDC identity changed")
		}
		replay := request(s, "GET", "/auth/callback?code=valid-code&state="+state, nil, cookie, nil)
		expect(t, replay, 400)
	}
	var count int
	s.db.QueryRow("SELECT count(*) FROM users").Scan(&count)
	if count != 1 {
		t.Fatalf("created %d users", count)
	}
	other, err := s.ensureUser(t.Context(), "https://different.example", "subject-1")
	if err != nil || other.ID == firstID {
		t.Fatal("issuer must scope subject")
	}
}
func TestOIDCRejectsInvalidTokens(t *testing.T) {
	for _, kind := range []string{"state", "nonce", "issuer", "audience", "expired", "signature", "flow-expired", "missing-cookie"} {
		t.Run(kind, func(t *testing.T) {
			s, p := testServer(t)
			cookie, state := beginOIDC(t, s, p)
			status := 401
			p.mu.Lock()
			switch kind {
			case "state":
				state = "wrong"
				status = 400
			case "nonce":
				p.overrides["nonce"] = "wrong"
			case "issuer":
				p.overrides["iss"] = "https://evil.example"
			case "audience":
				p.overrides["aud"] = "other"
			case "expired":
				p.overrides["exp"] = time.Now().Add(-time.Hour).Unix()
			case "signature":
				p.badSignature = true
			case "flow-expired":
				s.db.Exec("UPDATE oidc_flows SET expires=0")
				status = 400
			case "missing-cookie":
				cookie = nil
				status = 400
			}
			p.mu.Unlock()
			expect(t, request(s, "GET", "/auth/callback?code=valid-code&state="+state, nil, cookie, nil), status)
			var n int
			s.db.QueryRow("SELECT count(*) FROM users").Scan(&n)
			if n != 0 {
				t.Fatal("invalid OIDC login created user")
			}
		})
	}
}
func TestTokenLifecycleAndSessionProof(t *testing.T) {
	s, _ := testServer(t)
	u, key := seed(t, s, "a", "Alice")
	v := loginGame(t, s, "alice", key, "client")
	if len(v["availableProfiles"].([]any)) != 1 || v["selectedProfile"].(map[string]any)["id"] != u.ID {
		t.Fatal("single profile contract")
	}
	raw := v["accessToken"].(string)
	validate := func(token, client string, intended int) {
		expect(
			t,
			jsonReq(
				s,
				"/api/yggdrasil/authserver/validate",
				map[string]string{"accessToken": token, "clientToken": client},
			),
			intended,
		)
	}
	validate(raw, "wrong", 403)
	validate(raw, "client", 204)
	expect(
		t,
		jsonReq(
			s,
			"/api/yggdrasil/sessionserver/session/minecraft/join",
			map[string]string{"accessToken": raw, "selectedProfile": newUUID(), "serverId": "abc"},
		),
		403,
	)
	expect(
		t,
		jsonReq(
			s,
			"/api/yggdrasil/sessionserver/session/minecraft/join",
			map[string]string{"accessToken": raw, "selectedProfile": u.ID, "serverId": "abc"},
		),
		204,
	)
	expect(
		t,
		request(
			s,
			"GET",
			"/api/yggdrasil/sessionserver/session/minecraft/hasJoined?username=Alice&serverId=wrong",
			nil,
			nil,
			nil,
		),
		204,
	)
	expect(
		t,
		request(
			s,
			"GET",
			"/api/yggdrasil/sessionserver/session/minecraft/hasJoined?username=Alice&serverId=abc&ip=192.0.2.2",
			nil,
			nil,
			nil,
		),
		204,
	)
	w := request(
		s,
		"GET",
		"/api/yggdrasil/sessionserver/session/minecraft/hasJoined?username=Alice&serverId=abc&ip=192.0.2.1",
		nil,
		nil,
		nil,
	)
	expect(t, w, 200)
	verifyProfile(t, s, unmarshal[profile](t, w))
	expect(
		t,
		request(
			s,
			"GET",
			"/api/yggdrasil/sessionserver/session/minecraft/hasJoined?username=Alice&serverId=abc&ip=::ffff:192.0.2.1",
			nil,
			nil,
			nil,
		),
		200,
	)
	expect(
		t,
		request(
			s,
			"GET",
			"/api/yggdrasil/sessionserver/session/minecraft/hasJoined?username=Alice&serverId=abc&ip=invalid",
			nil,
			nil,
			nil,
		),
		204,
	)
	expect(
		t,
		jsonReq(s, "/api/yggdrasil/authserver/refresh", map[string]string{"accessToken": raw, "clientToken": "wrong"}),
		403,
	)
	validate(raw, "client", 204)
	expect(
		t,
		jsonReq(
			s,
			"/api/yggdrasil/authserver/refresh",
			map[string]any{"accessToken": raw, "selectedProfile": basicProfile(u)},
		),
		400,
	)
	validate(raw, "client", 204)
	w = jsonReq(s, "/api/yggdrasil/authserver/refresh", map[string]string{"accessToken": raw})
	expect(t, w, 200)
	next := unmarshal[map[string]any](t, w)
	fresh := next["accessToken"].(string)
	if next["clientToken"] != "client" || fresh == raw {
		t.Fatal("refresh did not rotate")
	}
	validate(raw, "client", 403)
	validate(fresh, "client", 204)
	expect(
		t,
		request(
			s,
			"GET",
			"/api/yggdrasil/sessionserver/session/minecraft/hasJoined?username=Alice&serverId=abc",
			nil,
			nil,
			nil,
		),
		204,
	)
	expect(
		t,
		jsonReq(
			s,
			"/api/yggdrasil/authserver/invalidate",
			map[string]string{"accessToken": fresh, "clientToken": "ignored"},
		),
		204,
	)
	validate(fresh, "client", 403)
	expect(t, jsonReq(s, "/api/yggdrasil/authserver/invalidate", map[string]string{"accessToken": "missing"}), 204)
	a := loginGame(t, s, "Alice", key, "a")["accessToken"].(string)
	b := loginGame(t, s, "Alice", key, "b")["accessToken"].(string)
	expect(
		t,
		jsonReq(s, "/api/yggdrasil/authserver/signout", map[string]string{"username": "Alice", "password": key}),
		204,
	)
	validate(a, "a", 403)
	validate(b, "b", 403)
}
func verifyProfile(t *testing.T, s *Server, p profile) {
	t.Helper()
	for _, prop := range p.Properties {
		sig, err := base64.StdEncoding.DecodeString(prop.Signature)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha1.Sum([]byte(prop.Value))
		if err = rsa.VerifyPKCS1v15(&s.key.PublicKey, crypto.SHA1, hash[:], sig); err != nil {
			t.Fatal("bad texture signature", err)
		}
	}
}
func TestConcurrentRefreshAndTokenLimit(t *testing.T) {
	s, _ := testServer(t)
	_, key := seed(t, s, "a", "Alice")
	raw := loginGame(t, s, "Alice", key, "c")["accessToken"].(string)
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for range 8 {
		wg.Go(func() {
			statuses <- jsonReq(s, "/api/yggdrasil/authserver/refresh", map[string]string{"accessToken": raw}).Code
		})
	}
	wg.Wait()
	close(statuses)
	successes := 0
	for status := range statuses {
		if status == 200 {
			successes++
		} else if status != 403 {
			t.Fatal(status)
		}
	}
	if successes != 1 {
		t.Fatalf("%d refreshes succeeded", successes)
	}
	for i := range 12 {
		loginGame(t, s, "Alice", key, strconv.Itoa(i))
	}
	var count int
	s.db.QueryRow("SELECT count(*) FROM tokens").Scan(&count)
	if count != 10 {
		t.Fatal("token bound", count)
	}
}
func TestRenameCSRFAndKeyRotation(t *testing.T) {
	s, _ := testServer(t)
	u, key := seed(t, s, "a", "Alice")
	seed(t, s, "b", "Bob")
	cookie, csrf := browser(t, s, u)
	raw := loginGame(t, s, "Alice", key, "c")["accessToken"].(string)
	expect(t, formReq(s, "/account/profile", url.Values{"name": {"NewAlice"}}, cookie), 403)
	expect(t, formReq(s, "/account/profile", url.Values{"name": {"bob"}, "csrf": {csrf}}, cookie), 409)
	expect(t, formReq(s, "/account/profile", url.Values{"name": {"bad name"}, "csrf": {csrf}}, cookie), 400)
	expect(t, formReq(s, "/account/profile", url.Values{"name": {"NewAlice"}, "csrf": {csrf}}, cookie), 303)
	renamed, err := s.userByName(t.Context(), "NewAlice")
	if err != nil || renamed.ID != u.ID {
		t.Fatal("rename changed UUID")
	}
	expect(t, jsonReq(s, "/api/yggdrasil/authserver/validate", map[string]string{"accessToken": raw}), 403)
	refreshed := jsonReq(s, "/api/yggdrasil/authserver/refresh", map[string]string{"accessToken": raw})
	expect(t, refreshed, 200)
	token := unmarshal[map[string]any](t, refreshed)
	if token["selectedProfile"].(map[string]any)["name"] != "NewAlice" {
		t.Fatal("stale name")
	}
	raw = token["accessToken"].(string)
	expect(t, formReq(s, "/account/launcher-key", url.Values{"csrf": {csrf}}, cookie), 200)
	expect(t, jsonReq(s, "/api/yggdrasil/authserver/validate", map[string]string{"accessToken": raw}), 403)
	expect(
		t,
		jsonReq(
			s,
			"/api/yggdrasil/authserver/authenticate",
			map[string]string{"username": "NewAlice", "password": key},
		),
		403,
	)
	expect(t, formReq(s, "/auth/logout", url.Values{"csrf": {csrf}}, cookie), 303)
	expect(t, formReq(s, "/account/launcher-key", url.Values{"csrf": {csrf}}, cookie), 401)
}
func pngData(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.Set(8, 8, color.NRGBA{R: 150, G: 80, B: 60, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func multipartBody(t *testing.T, data []byte, model, csrf string) (*bytes.Buffer, string) {
	t.Helper()
	b := new(bytes.Buffer)
	mw := multipart.NewWriter(b)
	f, err := mw.CreateFormFile("file", "skin.png")
	if err != nil {
		t.Fatal(err)
	}
	f.Write(data)
	mw.WriteField("model", model)
	if csrf != "" {
		mw.WriteField("csrf", csrf)
	}
	mw.Close()
	return b, mw.FormDataContentType()
}
func TestSkinUploadAndSignatures(t *testing.T) {
	s, _ := testServer(t)
	u, key := seed(t, s, "a", "Alice")
	other, _ := seed(t, s, "b", "Bob")
	raw := loginGame(t, s, "Alice", key, "c")["accessToken"].(string)
	path := "/api/yggdrasil/api/user/profile/" + u.ID + "/skin"
	upload := func(path string, data []byte, model string) *httptest.ResponseRecorder {
		b, ct := multipartBody(t, data, model, "")
		return request(s, "PUT", path, b, nil, map[string]string{"Content-Type": ct, "Authorization": "Bearer " + raw})
	}
	expect(t, upload(path, []byte("not png"), "slim"), 400)
	expect(t, upload(path, pngData(t, 128, 128), "default"), 400)
	expect(t, upload(path, pngData(t, 64, 32), "slim"), 400)
	expect(t, upload(path, pngData(t, 64, 64), "unknown"), 400)
	expect(t, upload(path, make([]byte, (1<<20)+1), "default"), 400)
	expect(t, upload("/api/yggdrasil/api/user/profile/"+other.ID+"/skin", pngData(t, 64, 64), "slim"), 403)
	original := pngData(t, 64, 64)
	withJunk := append(append([]byte{}, original...), []byte("injected-metadata")...)
	expect(t, upload(path, withJunk, "slim"), 204)
	u, _ = s.userByID(t.Context(), u.ID)
	w := request(s, "GET", "/textures/"+u.SkinHash, nil, nil, nil)
	expect(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), original) || bytes.Contains(w.Body.Bytes(), []byte("injected")) {
		t.Fatal("not canonical PNG")
	}
	if w.Header().Get("Content-Type") != "image/png" ||
		!strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal("bad texture headers")
	}
	expect(
		t,
		request(
			s,
			"GET",
			"/textures/"+u.SkinHash,
			nil,
			nil,
			map[string]string{"If-None-Match": w.Header().Get("ETag")},
		),
		304,
	)
	w = request(
		s,
		"GET",
		"/api/yggdrasil/sessionserver/session/minecraft/profile/"+u.ID+"?unsigned=false",
		nil,
		nil,
		nil,
	)
	expect(t, w, 200)
	p := unmarshal[profile](t, w)
	verifyProfile(t, s, p)
	encoded, _ := base64.StdEncoding.DecodeString(p.Properties[0].Value)
	var payload map[string]any
	json.Unmarshal(encoded, &payload)
	skin := payload["textures"].(map[string]any)["SKIN"].(map[string]any)
	if skin["url"] != s.cfg.BaseURL+"/textures/"+u.SkinHash || skin["metadata"].(map[string]any)["model"] != "slim" {
		t.Fatal("bad skin payload")
	}
	w = request(s, "GET", "/api/yggdrasil/sessionserver/session/minecraft/profile/"+u.ID, nil, nil, nil)
	expect(t, w, 200)
	if unmarshal[profile](t, w).Properties[0].Signature != "" {
		t.Fatal("unsigned default")
	}
	expect(t, request(s, "DELETE", path, nil, nil, map[string]string{"Authorization": "Bearer " + raw}), 204)
	u, _ = s.userByID(t.Context(), u.ID)
	if u.SkinHash != "" {
		t.Fatal("skin not deleted")
	}
	cookie, csrf := browser(t, s, u)
	b, ct := multipartBody(t, pngData(t, 64, 32), "default", csrf)
	expect(
		t,
		request(s, "POST", "/account/skin", b, cookie, map[string]string{"Content-Type": ct, "Origin": s.cfg.BaseURL}),
		303,
	)
	u, _ = s.userByID(t.Context(), u.ID)
	if u.SkinHash == "" {
		t.Fatal("web upload missing")
	}
	expect(t, request(s, "PUT", path, nil, nil, nil), 401)
	expect(t, upload(strings.TrimSuffix(path, "skin")+"cape", original, "default"), 400)
}
func TestMetadataPersistenceExpirationAndProxy(t *testing.T) {
	s, _ := testServer(t)
	u, key := seed(t, s, "a", "Alice")
	expect(t, request(s, "GET", "/api/yggdrasil", nil, nil, nil), 200)
	w := request(s, "GET", "/api/yggdrasil/", nil, nil, nil)
	expect(t, w, 200)
	metadata := unmarshal[map[string]any](t, w)
	block, _ := pem.Decode([]byte(metadata["signaturePublickey"].(string)))
	if block == nil {
		t.Fatal("missing public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil || pub.(*rsa.PublicKey).N.Cmp(s.key.N) != 0 {
		t.Fatal("wrong public key")
	}
	reopened, err := openDB(t.Context(), s.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := loadKey(t.Context(), reopened)
	if err != nil || persisted.N.Cmp(s.key.N) != 0 {
		t.Fatal("key not persistent")
	}
	raw := loginGame(t, s, "Alice", key, "c")["accessToken"].(string)
	expect(
		t,
		jsonReq(
			s,
			"/api/yggdrasil/sessionserver/session/minecraft/join",
			map[string]string{"accessToken": raw, "selectedProfile": u.ID, "serverId": "x"},
		),
		204,
	)
	s.db.Exec("UPDATE joins SET expires=0")
	expect(
		t,
		request(
			s,
			"GET",
			"/api/yggdrasil/sessionserver/session/minecraft/hasJoined?username=Alice&serverId=x",
			nil,
			nil,
			nil,
		),
		204,
	)
	s.db.Exec("UPDATE tokens SET expires=0")
	expect(t, jsonReq(s, "/api/yggdrasil/authserver/refresh", map[string]string{"accessToken": raw}), 403)
	if err = s.cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Real-IP", "203.0.113.2")
	if s.clientIP(req) != "192.0.2.1" {
		t.Fatal("trusted spoofed header")
	}
	s.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}
	if s.clientIP(req) != "203.0.113.2" {
		t.Fatal("trusted proxy not honored")
	}
	expect(t, jsonReq(s, "/api/yggdrasil/api/profiles/minecraft", []string{"ALICE", "Alice", "missing"}), 200)
	profiles := unmarshal[[]profile](
		t,
		jsonReq(s, "/api/yggdrasil/api/profiles/minecraft", []string{"ALICE", "Alice", "missing"}),
	)
	if len(profiles) != 1 {
		t.Fatal("lookup deduplication")
	}
	expect(t, jsonReq(s, "/api/yggdrasil/api/profiles/minecraft", make([]string, 101)), 400)
	expect(
		t,
		request(s, "GET", "/api/yggdrasil/sessionserver/session/minecraft/profile/"+newUUID(), nil, nil, nil),
		204,
	)
}
func TestRequestLimitsAndBrowserProtection(t *testing.T) {
	s, _ := testServer(t)
	u, _ := seed(t, s, "a", "Alice")
	cookie, csrf := browser(t, s, u)
	expect(
		t,
		request(
			s,
			"POST",
			"/account/profile",
			strings.NewReader(url.Values{"name": {"Changed"}, "csrf": {csrf}}.Encode()),
			cookie,
			map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "https://evil.example"},
		),
		403,
	)
	for _, input := range []string{"{", "{}{}", strings.Repeat("x", 70<<10)} {
		expect(t, request(s, "POST", "/api/yggdrasil/authserver/authenticate", strings.NewReader(input), nil, nil), 400)
	}
	for range 20 {
		expect(
			t,
			jsonReq(
				s,
				"/api/yggdrasil/authserver/authenticate",
				map[string]string{"username": "Alice", "password": "bad"},
			),
			403,
		)
	}
	expect(
		t,
		jsonReq(s, "/api/yggdrasil/authserver/signout", map[string]string{"username": "alice", "password": "bad"}),
		429,
	)
	w := request(s, "GET", "/", nil, nil, nil)
	expect(t, w, 200)
	if w.Header().Get("X-Authlib-Injector-Api-Location") != s.cfg.BaseURL+"/api/yggdrasil/" ||
		w.Header().Get("Content-Security-Policy") == "" ||
		w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("missing security/discovery headers")
	}
	s.db.Exec("UPDATE web_sessions SET expires=0")
	expect(t, formReq(s, "/account/profile", url.Values{"name": {"Changed"}, "csrf": {csrf}}, cookie), 401)
}
func TestConfigValidation(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://example.com/sub", "https://user:pw@example.com", "https://example.com?x=1", ""} {
		c := Config{BaseURL: raw, Issuer: "https://id.example.com", ClientID: "c", DataDir: t.TempDir()}
		if c.Validate() == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestRenameUsesCurrentNameDuringConcurrentRequests(t *testing.T) {
	s, _ := testServer(t)
	u, key := seed(t, s, "a", "Alice")
	// Simulate an already authenticated request that paused while another request renamed the user.
	staleSession := session{User: u}
	if _, err := s.db.Exec("UPDATE users SET name='Renamed' WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	raw := loginGame(t, s, "Renamed", key, "client")["accessToken"].(string)
	r := httptest.NewRequest(http.MethodPost, "/account/profile", strings.NewReader("name=Alice"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.rename(w, r, staleSession)
	expect(t, w, 303)
	expect(t, jsonReq(s, "/api/yggdrasil/authserver/validate", map[string]string{"accessToken": raw}), 403)
	refreshed := jsonReq(s, "/api/yggdrasil/authserver/refresh", map[string]string{"accessToken": raw})
	expect(t, refreshed, 200)
	if unmarshal[map[string]any](t, refreshed)["selectedProfile"].(map[string]any)["name"] != "Alice" {
		t.Fatal("refresh must use current name")
	}
}
