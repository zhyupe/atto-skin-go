package server

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zhyupe/atto-skin-go/internal/database/queries"
	"github.com/zhyupe/atto-skin-go/internal/validation"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func (s *Server) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(
		w,
		&http.Cookie{ //nolint:gosec // Config.Validate permits insecure cookies only on loopback HTTP.
			Name:     name,
			Value:    value,
			Path:     "/",
			MaxAge:   age,
			HttpOnly: true,
			Secure:   strings.HasPrefix(s.cfg.BaseURL, "https://"),
			SameSite: http.SameSiteLaxMode,
		},
	)
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.allow("oidc:"+s.clientIP(r), 20) {
		http.Error(w, "登录请求过多，请稍后重试。", http.StatusTooManyRequests)
		return
	}
	browser, state, nonce, verifier := randomHex(), randomHex(), randomHex(), oauth2.GenerateVerifier()
	if old, err := r.Cookie("atto_flow"); err == nil {
		if _, err = queries.New(s.db).DeleteOIDCFlow(r.Context(), digest(old.Value)); err != nil {
			internalError(r.Context(), w, err)
			return
		}
	}
	_, err := queries.New(s.db).
		CreateOIDCFlow(r.Context(), queries.CreateOIDCFlowParams{Hash: digest(browser), State: state, Nonce: nonce, Verifier: verifier, Expires: time.Now().Add(10 * time.Minute).Unix()})
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	s.cookie(w, "atto_flow", browser, 600)
	http.Redirect(
		w,
		r,
		s.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)),
		http.StatusFound,
	)
}
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("atto_flow")
	if err != nil {
		http.Error(w, "登录会话已失效，请重新登录。", http.StatusBadRequest)
		return
	}
	var state, nonce, verifier string
	var expires int64
	err = s.db.QueryRowContext(r.Context(), queries.ConsumeOIDCFlow, digest(c.Value)).
		Scan(&state, &nonce, &verifier, &expires)
	s.cookie(w, "atto_flow", "", -1)
	if err != nil || expires <= time.Now().Unix() ||
		subtle.ConstantTimeCompare([]byte(state), []byte(r.URL.Query().Get("state"))) != 1 ||
		r.URL.Query().Get("code") == "" {
		http.Error(w, "登录校验失败，请重新登录。", http.StatusBadRequest)
		return
	}
	ctx := oidc.ClientContext(r.Context(), s.httpClient)
	token, err := s.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		http.Error(w, "无法完成身份提供方登录，请重试。", http.StatusBadGateway)
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "身份提供方未返回 ID token。", http.StatusBadGateway)
		return
	}
	id, err := s.verifier.Verify(ctx, raw)
	if err != nil || id.Subject == "" || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(nonce)) != 1 {
		http.Error(w, "身份验证失败。", http.StatusUnauthorized)
		return
	}
	u, err := s.ensureUser(r.Context(), id.Issuer, id.Subject)
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	if old, e := r.Cookie("atto_session"); e == nil {
		if _, err = queries.New(s.db).DeleteLegacySession(r.Context(), digest(old.Value)); err != nil {
			internalError(r.Context(), w, err)
			return
		}
	}
	if err = s.saveSession(w, r, u); err != nil {
		internalError(r.Context(), w, err)
		return
	}
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
		return v, fmt.Errorf("session: %w", err)
	}
	if !hashPattern.MatchString(c.Value) {
		return s.encryptedSession(r)
	}
	v.Hash = digest(c.Value)
	var uid string
	err = s.db.QueryRowContext(r.Context(), queries.GetLegacySession, v.Hash, time.Now().Unix()).Scan(&uid, &v.CSRF)
	if err != nil {
		return v, fmt.Errorf("session: %w", err)
	}
	v.User, err = s.userByID(r.Context(), uid)
	if err != nil {
		return v, fmt.Errorf("session: %w", err)
	}
	return v, nil
}
func (s *Server) withSession(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := s.session(r)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, http.ErrNoCookie) && !errors.Is(err, errNoSession) {
				internalError(r.Context(), w, err)
				return
			}
			http.Error(w, "请先登录。", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, (1<<20)+65536)
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.cfg.BaseURL {
			http.Error(w, "请求来源无效。", http.StatusForbidden)
			return
		}
		if err = s.csrf.Check(r); err != nil {
			http.Error(w, "请求来源无效。", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err = r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, "上传失败：文件或表单过大。", http.StatusBadRequest)
				return
			}
			defer func() { _ = r.MultipartForm.RemoveAll() }()
		} else if err = r.ParseForm(); err != nil {
			http.Error(w, "表单无效。", http.StatusBadRequest)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(v.CSRF)) != 1 {
			http.Error(w, "页面已失效，请刷新后重试。", http.StatusForbidden)
			return
		}
		next(w, r, v)
	}
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request, v session) {
	if v.Hash != "" {
		if _, err := queries.New(s.db).DeleteLegacySession(r.Context(), v.Hash); err != nil {
			internalError(r.Context(), w, err)
			return
		}
	} else if _, err := queries.New(s.db).RevokeUserCookies(r.Context(), v.User.ID); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	s.cookie(w, "atto_session", "", -1)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) rename(w http.ResponseWriter, r *http.Request, v session) {
	name := r.PostFormValue("name")
	if !validation.PlayerName(name) {
		s.render(r.Context(), w, v, "角色名须为 3–16 位英文字母、数字或下划线。", "", 400)
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var existing string
	err = tx.QueryRowContext(r.Context(), queries.FindUserIDByName, name).Scan(&existing)
	if err == nil && existing != v.User.ID {
		_ = tx.Rollback()
		s.render(r.Context(), w, v, "这个角色名已被使用。", "", 409)
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		internalError(r.Context(), w, err)
		return
	}
	var previousName string
	if err = tx.QueryRowContext(r.Context(), queries.GetUserName, v.User.ID).Scan(&previousName); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	_, err = queries.New(tx).RenameUser(r.Context(), queries.RenameUserParams{Name: name, ID: v.User.ID})
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	if previousName != name {
		if _, err = queries.New(tx).StaleUserTokens(r.Context(), v.User.ID); err != nil {
			internalError(r.Context(), w, err)
			return
		}
		if _, err = queries.New(tx).DeleteUserJoins(r.Context(), v.User.ID); err != nil {
			internalError(r.Context(), w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	http.Redirect(w, r, "/?saved=profile", http.StatusSeeOther)
}
func (s *Server) rotateKey(w http.ResponseWriter, r *http.Request, v session) {
	key := randomHex()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = queries.New(tx).
		SetLauncherSecret(r.Context(), queries.SetLauncherSecretParams{SecretHash: digest(key), ID: v.User.ID}); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	if _, err = queries.New(tx).DeleteUserTokens(r.Context(), v.User.ID); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	v.User.SecretHash = digest(key)
	s.render(r.Context(), w, v, "启动器密钥已更新，旧密钥和游戏会话已失效。", key, 200)
}
