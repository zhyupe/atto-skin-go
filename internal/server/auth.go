package server

import (
	"crypto/subtle"
	"database/sql"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func (s *Server) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: strings.HasPrefix(s.cfg.BaseURL, "https://"), SameSite: http.SameSiteLaxMode})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.allow("oidc:"+s.clientIP(r), 20) {
		http.Error(w, "登录请求过多，请稍后重试。", 429)
		return
	}
	browser, state, nonce, verifier := randomHex(32), randomHex(32), randomHex(32), oauth2.GenerateVerifier()
	if old, err := r.Cookie("atto_flow"); err == nil {
		if _, err = s.db.Exec("DELETE FROM oidc_flows WHERE hash=?", digest(old.Value)); err != nil {
			internalError(w, err)
			return
		}
	}
	_, err := s.db.Exec("INSERT INTO oidc_flows VALUES(?,?,?,?,?)", digest(browser), state, nonce, verifier, time.Now().Add(10*time.Minute).Unix())
	if err != nil {
		internalError(w, err)
		return
	}
	s.cookie(w, "atto_flow", browser, 600)
	http.Redirect(w, r, s.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("atto_flow")
	if err != nil {
		http.Error(w, "登录会话已失效，请重新登录。", 400)
		return
	}
	var state, nonce, verifier string
	var expires int64
	err = s.db.QueryRow("DELETE FROM oidc_flows WHERE hash=? RETURNING state,nonce,verifier,expires", digest(c.Value)).Scan(&state, &nonce, &verifier, &expires)
	s.cookie(w, "atto_flow", "", -1)
	if err != nil || expires <= time.Now().Unix() || subtle.ConstantTimeCompare([]byte(state), []byte(r.URL.Query().Get("state"))) != 1 || r.URL.Query().Get("code") == "" {
		http.Error(w, "登录校验失败，请重新登录。", 400)
		return
	}
	ctx := oidc.ClientContext(r.Context(), s.httpClient)
	token, err := s.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		http.Error(w, "无法完成身份提供方登录，请重试。", 502)
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "身份提供方未返回 ID token。", 502)
		return
	}
	id, err := s.verifier.Verify(ctx, raw)
	if err != nil || id.Subject == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(nonce)) != 1 {
		http.Error(w, "身份验证失败。", 401)
		return
	}
	u, err := s.ensureUser(id.Issuer, id.Subject)
	if err != nil {
		internalError(w, err)
		return
	}
	session := randomHex(32)
	tx, err := s.db.Begin()
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback()
	if old, e := r.Cookie("atto_session"); e == nil {
		_, err = tx.Exec("DELETE FROM web_sessions WHERE hash=?", digest(old.Value))
		if err != nil {
			internalError(w, err)
			return
		}
	}
	_, err = tx.Exec("INSERT INTO web_sessions VALUES(?,?,?,?)", digest(session), u.ID, randomHex(32), time.Now().Add(24*time.Hour).Unix())
	if err != nil {
		internalError(w, err)
		return
	}
	_, err = tx.Exec("DELETE FROM web_sessions WHERE user_id=? AND rowid NOT IN (SELECT rowid FROM web_sessions WHERE user_id=? ORDER BY rowid DESC LIMIT 10)", u.ID, u.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, err)
		return
	}
	s.cookie(w, "atto_session", session, 86400)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type session struct {
	User       user
	CSRF, Hash string
}

func (s *Server) session(r *http.Request) (session, error) {
	var v session
	c, err := r.Cookie("atto_session")
	if err != nil {
		return v, err
	}
	v.Hash = digest(c.Value)
	var uid string
	err = s.db.QueryRow("SELECT user_id,csrf FROM web_sessions WHERE hash=? AND expires>?", v.Hash, time.Now().Unix()).Scan(&uid, &v.CSRF)
	if err != nil {
		return v, err
	}
	v.User, err = s.userByID(uid)
	return v, err
}
func (s *Server) withSession(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := s.session(r)
		if err != nil {
			if err != sql.ErrNoRows && err != http.ErrNoCookie {
				internalError(w, err)
				return
			}
			http.Error(w, "请先登录。", 401)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, (1<<20)+65536)
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.cfg.BaseURL {
			http.Error(w, "请求来源无效。", 403)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "请求来源无效。", 403)
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err = r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, "上传失败：文件或表单过大。", 400)
				return
			}
			defer r.MultipartForm.RemoveAll()
		} else if err = r.ParseForm(); err != nil {
			http.Error(w, "表单无效。", 400)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(v.CSRF)) != 1 {
			http.Error(w, "页面已失效，请刷新后重试。", 403)
			return
		}
		next(w, r, v)
	}
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request, v session) {
	if _, err := s.db.Exec("DELETE FROM web_sessions WHERE hash=?", v.Hash); err != nil {
		internalError(w, err)
		return
	}
	s.cookie(w, "atto_session", "", -1)
	http.Redirect(w, r, "/", 303)
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)

func (s *Server) rename(w http.ResponseWriter, r *http.Request, v session) {
	name := r.PostFormValue("name")
	if !namePattern.MatchString(name) {
		s.render(w, v, "角色名须为 3–16 位英文字母、数字或下划线。", "", 400)
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow("SELECT id FROM users WHERE name=? COLLATE NOCASE", name).Scan(&existing)
	if err == nil && existing != v.User.ID {
		tx.Rollback()
		s.render(w, v, "这个角色名已被使用。", "", 409)
		return
	}
	if err != nil && err != sql.ErrNoRows {
		internalError(w, err)
		return
	}
	var previousName string
	if err = tx.QueryRow("SELECT name FROM users WHERE id=?", v.User.ID).Scan(&previousName); err != nil {
		internalError(w, err)
		return
	}
	_, err = tx.Exec("UPDATE users SET name=? WHERE id=?", name, v.User.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	if previousName != name {
		if _, err = tx.Exec("UPDATE tokens SET stale=1 WHERE user_id=?", v.User.ID); err != nil {
			internalError(w, err)
			return
		}
		if _, err = tx.Exec("DELETE FROM joins WHERE user_id=?", v.User.ID); err != nil {
			internalError(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		internalError(w, err)
		return
	}
	http.Redirect(w, r, "/?saved=profile", 303)
}
func (s *Server) rotateKey(w http.ResponseWriter, r *http.Request, v session) {
	key := randomHex(32)
	tx, err := s.db.Begin()
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE users SET secret_hash=? WHERE id=?", digest(key), v.User.ID); err != nil {
		internalError(w, err)
		return
	}
	if _, err = tx.Exec("DELETE FROM tokens WHERE user_id=?", v.User.ID); err != nil {
		internalError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, err)
		return
	}
	v.User.SecretHash = digest(key)
	s.render(w, v, "启动器密钥已更新，旧密钥和游戏会话已失效。", key, 200)
}
