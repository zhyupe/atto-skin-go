package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestSkinHistoryUploadRestoreAndIsolation(t *testing.T) {
	s, _ := testServer(t)
	u, key := seed(t, s, "history-a", "Alice")
	other, _ := seed(t, s, "history-b", "Bob")
	cookie, csrf := browser(t, s, u)
	otherCookie, otherCSRF := browser(t, s, other)
	original := pngData(t, 64, 64)
	b, ct := multipartBody(t, original, "slim", csrf)
	expect(t, request(s, "POST", "/account/skin", b, cookie, map[string]string{"Content-Type": ct}), 303)
	u, _ = s.userByID(u.ID)
	hash := u.SkinHash
	raw := loginGame(t, s, "Alice", key, "c")["accessToken"].(string)
	path := "/api/yggdrasil/api/user/profile/" + u.ID + "/skin"
	// Same PNG with two arm models must remain two distinct selections, without duplicates.
	for i := 0; i < 2; i++ {
		b, ct = multipartBody(t, original, "default", "")
		expect(t, request(s, "PUT", path, b, nil, map[string]string{"Content-Type": ct, "Authorization": "Bearer " + raw}), 204)
	}
	u, _ = s.userByID(u.ID)
	items, more, err := s.skinHistory(u, 0)
	if err != nil || more || len(items) != 2 || !items[0].Current || items[0].Model != "default" {
		t.Fatalf("history: %+v %v %v", items, more, err)
	}
	restore := url.Values{"csrf": {csrf}, "hash": {hash}, "model": {"slim"}}
	expect(t, formReq(s, "/account/skin/restore", restore, nil), 401)
	expect(t, formReq(s, "/account/skin/restore", url.Values{"hash": {hash}, "model": {"slim"}}, cookie), 403)
	expect(t, formReq(s, "/account/skin/restore", url.Values{"csrf": {otherCSRF}, "hash": {hash}, "model": {"slim"}}, otherCookie), 404)
	otherHome := request(s, "GET", "/", nil, otherCookie, nil)
	expect(t, otherHome, 200)
	if strings.Contains(otherHome.Body.String(), hash) {
		t.Fatal("another user's history leaked")
	}
	expect(t, formReq(s, "/account/skin/restore", url.Values{"csrf": {csrf}, "hash": {strings.Repeat("f", 64)}, "model": {"slim"}}, cookie), 404)
	expect(t, formReq(s, "/account/skin/restore", url.Values{"csrf": {csrf}, "hash": {hash}, "model": {"invalid"}}, cookie), 400)
	// Both ways of returning to the default appearance retain history.
	expect(t, request(s, "DELETE", path, nil, nil, map[string]string{"Authorization": "Bearer " + raw}), 204)
	expect(t, formReq(s, "/account/skin/restore", restore, cookie), 303)
	expect(t, formReq(s, "/account/skin/delete", url.Values{"csrf": {csrf}}, cookie), 303)
	expect(t, formReq(s, "/account/skin/restore", restore, cookie), 303)
	u, _ = s.userByID(u.ID)
	items, _, err = s.skinHistory(u, 0)
	if err != nil || len(items) != 2 || !items[0].Current || items[0].Model != "slim" || u.SkinHash != hash {
		t.Fatalf("restored history: %+v %v", items, err)
	}
	home := request(s, "GET", "/", nil, cookie, nil)
	expect(t, home, 200)
	for _, text := range []string{"历史皮肤", "使用此皮肤", "正在使用", "</html>"} {
		if !strings.Contains(home.Body.String(), text) {
			t.Fatalf("missing rendered content %q", text)
		}
	}
	w := request(s, "GET", "/api/yggdrasil/sessionserver/session/minecraft/profile/"+u.ID+"?unsigned=false", nil, nil, nil)
	expect(t, w, 200)
	p := unmarshal[profile](t, w)
	verifyProfile(t, s, p)
	decoded, err := base64.StdEncoding.DecodeString(p.Properties[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Textures map[string]struct {
			URL      string
			Metadata map[string]string
		}
	}
	if err = json.Unmarshal(decoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Textures["SKIN"].URL != s.cfg.BaseURL+"/textures/"+hash || payload.Textures["SKIN"].Metadata["model"] != "slim" {
		t.Fatal("protocol did not use restored skin/model")
	}
	expect(t, jsonReq(s, "/api/yggdrasil/authserver/validate", map[string]string{"accessToken": raw}), 204)
}

func TestHistoryPagination(t *testing.T) {
	s, _ := testServer(t)
	u, _ := seed(t, s, "pages", "Pages")
	for i := 0; i < 14; i++ {
		if err := s.setSkin(u.ID, []byte(fmt.Sprintf("fixture-%d", i)), "default", ""); err != nil {
			t.Fatal(err)
		}
	}
	u, _ = s.userByID(u.ID)
	first, more, err := s.skinHistory(u, 0)
	if err != nil || !more || len(first) != 12 || !first[0].Current {
		t.Fatal(first, more, err)
	}
	second, more, err := s.skinHistory(u, 1)
	if err != nil || more || len(second) != 2 {
		t.Fatal(second, more, err)
	}
	if second[0].Hash == first[11].Hash {
		t.Fatal("overlapping pages")
	}
	cookie, _ := browser(t, s, u)
	w := request(s, "GET", "/?history_page=1", nil, cookie, nil)
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), "上一页") || strings.Contains(w.Body.String(), "下一页") {
		t.Fatal("incorrect pagination controls")
	}
	for _, page := range []string{"-1", "invalid", "999999999999999999999999"} {
		expect(t, request(s, "GET", "/?history_page="+page, nil, cookie, nil), 400)
	}
}

func TestHistoryTransactionRollback(t *testing.T) {
	s, _ := testServer(t)
	u, _ := seed(t, s, "rollback", "Rollback")
	if err := s.setSkin(u.ID, pngData(t, 64, 64), "slim", ""); err != nil {
		t.Fatal(err)
	}
	old, _ := s.userByID(u.ID)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_history BEFORE INSERT ON skin_history WHEN NEW.last_used > 0 BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.setSkin(u.ID, pngData(t, 64, 32), "default", ""); err == nil {
		t.Fatal("expected failed history write")
	}
	afterUpload, err := s.userByID(u.ID)
	if err != nil || afterUpload.SkinHash != old.SkinHash || afterUpload.Model != old.Model {
		t.Fatal("failed upload changed selection", err)
	}
	// Also exercise rollback of the UPDATE-before-history-write restore path.
	if _, err := s.db.Exec("UPDATE users SET skin_hash='',model='default' WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.restoreSkin(u.ID, old.SkinHash, old.Model); err == nil {
		t.Fatal("expected restore rollback")
	}
	current, _ := s.userByID(u.ID)
	if current.SkinHash != "" {
		t.Fatal("failed restore partially changed current skin")
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM textures").Scan(&n); err != nil || n != 1 {
		t.Fatal("failed upload left texture", n, err)
	}
}

func TestHistoryUpgradeAndImport(t *testing.T) {
	dir := t.TempDir()
	p := migrationProfile("12345678123442348234123456789abc", "Alice", "history-import")
	p.Model, p.PNG = "slim", pngData(t, 64, 64)
	if _, err := ImportProfiles(context.Background(), dir, []ImportedProfile{p}, true); err != nil {
		t.Fatal(err)
	}
	// Inspect directly, before openDB could backfill a missing import record.
	db, err := sql.Open("sqlite", dir+"/atto.db")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow("SELECT count(*) FROM skin_history WHERE user_id=? AND model='slim'", p.ID).Scan(&n); err != nil || n != 1 {
		t.Fatal("import omitted history", n, err)
	}
	if _, err = db.Exec("DROP TABLE skin_history; PRAGMA user_version=1; UPDATE users SET secret_hash='keep-me';"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	// A dry run against v1 must not upgrade or modify the database.
	if _, err = ImportProfiles(context.Background(), dir, []ImportedProfile{p}, false); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", dir+"/atto.db")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("PRAGMA user_version").Scan(&n); err != nil || n != 1 {
		t.Fatal("dry run modified schema", n, err)
	}
	db.Close()
	for i := 0; i < 2; i++ {
		db, err = openDB(dir)
		if err != nil {
			t.Fatal(err)
		}
		app := &Server{db: db}
		u, err := app.userByID(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		items, _, err := app.skinHistory(u, 0)
		if err != nil || len(items) != 1 || !items[0].Current || items[0].Model != "slim" || u.SecretHash != "keep-me" {
			t.Fatal("upgrade/reopen lost data", items, err)
		}
		var png []byte
		if err = db.QueryRow("SELECT png FROM textures WHERE hash=?", u.SkinHash).Scan(&png); err != nil || !bytes.Equal(png, p.PNG) {
			t.Fatal("texture changed", err)
		}
		if err = db.QueryRow("PRAGMA user_version").Scan(&n); err != nil || n != 2 {
			t.Fatal("schema not upgraded", n, err)
		}
		db.Close()
	}
}
