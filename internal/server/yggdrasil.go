package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // Required by the Yggdrasil SHA1withRSA wire protocol.
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/zhyupe/atto-skin-go/internal/database/queries"
)

type profile struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Properties []property `json:"properties,omitempty"`
}
type property struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Signature string `json:"signature,omitempty"`
}

func basicProfile(u user) profile { return profile{ID: u.ID, Name: u.Name} }
func (s *Server) fullProfile(u user, signed bool) (profile, error) {
	textures := map[string]any{}
	if u.SkinHash != "" {
		textures["SKIN"] = map[string]any{
			"url":      s.cfg.BaseURL + "/textures/" + u.SkinHash,
			"metadata": map[string]string{"model": u.Model},
		}
	}
	payload := map[string]any{
		"timestamp":   time.Now().UnixMilli(),
		"profileId":   u.ID,
		"profileName": u.Name,
		"textures":    textures,
	}
	if signed {
		payload["signatureRequired"] = true
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return profile{}, fmt.Errorf("full profile: %w", err)
	}
	props := []property{
		{Name: "textures", Value: base64.StdEncoding.EncodeToString(b)},
		{Name: "uploadableTextures", Value: skinKind},
	}
	if signed {
		for i := range props {
			hash := sha1.Sum([]byte(props[i].Value)) //nolint:gosec // Yggdrasil requires SHA1withRSA signatures.
			sig, e := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA1, hash[:])
			if e != nil {
				return profile{}, fmt.Errorf("full profile: %w", e)
			}
			props[i].Signature = base64.StdEncoding.EncodeToString(sig)
		}
	}
	p := basicProfile(u)
	p.Properties = props
	return p, nil
}
func (s *Server) metadata(w http.ResponseWriter, _ *http.Request) {
	base, _ := url.Parse(s.cfg.BaseURL)
	jsonResponse(
		w,
		200,
		map[string]any{
			"meta": map[string]any{
				"serverName":              "Atto Skin",
				"implementationName":      "atto-skin-go",
				"implementationVersion":   "0.1.0",
				"feature.non_email_login": true,
				"links": map[string]string{
					"homepage": s.cfg.BaseURL,
					"register": s.cfg.BaseURL + "/auth/login",
				},
			},
			"skinDomains":        []string{base.Hostname()},
			"signaturePublickey": publicKey(s.key),
		},
	)
}

type authRequest struct {
	Username    string  `json:"username"`
	Password    string  `json:"password"`
	ClientToken *string `json:"clientToken"`
	RequestUser bool    `json:"requestUser"`
	Agent       *struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	} `json:"agent"`
}
type tokenRequest struct {
	AccessToken     string   `json:"accessToken"`
	ClientToken     *string  `json:"clientToken"`
	RequestUser     bool     `json:"requestUser"`
	SelectedProfile *profile `json:"selectedProfile"`
}
type gameToken struct {
	Hash, UserID, Client string
	Stale                bool
}

func forbidden(w http.ResponseWriter) {
	apiError(w, 403, "ForbiddenOperationException", "Invalid credentials or token.")
}
func (s *Server) credentials(w http.ResponseWriter, r *http.Request, req authRequest) (user, bool) {
	// Rate limits are shared by authenticate and signout, including by account name.
	if !s.allow("account:"+digest(strings.ToLower(req.Username)), 20) || !s.allow("auth-ip:"+s.clientIP(r), 100) {
		apiError(w, 429, "TooManyRequestsException", "Too many attempts; try again in one minute.")
		return user{}, false
	}
	u, err := s.userByName(r.Context(), req.Username)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		internalError(r.Context(), w, err)
		return user{}, false
	}
	candidate := digest(req.Password)
	matches := subtle.ConstantTimeCompare([]byte(candidate), []byte(u.SecretHash)) == 1
	if err != nil || u.SecretHash == "" || !matches {
		forbidden(w)
		return user{}, false
	}
	return u, true
}
func (s *Server) lookupToken(ctx context.Context, raw string, client *string, allowStale bool) (gameToken, error) {
	t := gameToken{Hash: digest(raw)}
	err := s.db.QueryRowContext(ctx, queries.LookupToken, t.Hash, time.Now().Unix()).
		Scan(&t.UserID, &t.Client, &t.Stale)
	if err != nil {
		return t, fmt.Errorf("lookup token: %w", err)
	}
	if (t.Stale && !allowStale) || (client != nil && *client != t.Client) {
		return t, sql.ErrNoRows
	}
	return t, nil
}
func insertToken(ctx context.Context, tx *sql.Tx, uid, client, raw string) error {
	now := time.Now()
	if _, err := queries.New(tx).
		CreateToken(ctx, queries.CreateTokenParams{Hash: digest(raw), UserID: uid, Client: client, Issued: now.Unix(), Expires: now.Add(15 * 24 * time.Hour).Unix()}); err != nil {
		return fmt.Errorf("insert token: %w", err)
	}
	_, err := queries.New(tx).PruneTokens(ctx, queries.PruneTokensParams{UserID: uid, UserID_2: uid})
	if err != nil {
		return fmt.Errorf("insert token: %w", err)
	}
	return nil
}
func authResponse(u user, access, client string, includeUser, available bool) map[string]any {
	v := map[string]any{"accessToken": access, "clientToken": client, "selectedProfile": basicProfile(u)}
	if available {
		v["availableProfiles"] = []profile{basicProfile(u)}
	}
	if includeUser {
		v["user"] = map[string]any{"id": u.ID, "properties": []property{}}
	}
	return v
}
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Agent != nil && (req.Agent.Name != "Minecraft" || req.Agent.Version != 1) {
		apiError(w, 400, "IllegalArgumentException", "Unsupported agent.")
		return
	}
	u, ok := s.credentials(w, r, req)
	if !ok {
		return
	}
	client := newUUID()
	if req.ClientToken != nil {
		client = *req.ClientToken
	}
	raw := randomHex()
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	// Recheck the secret under the write transaction so key rotation cannot race login.
	current, err := scanUser(tx.QueryRowContext(r.Context(), queries.GetUserBySecret, u.ID, u.SecretHash))
	if errors.Is(err, sql.ErrNoRows) {
		forbidden(w)
		return
	}
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	if err = insertToken(r.Context(), tx, current.ID, client, raw); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	jsonResponse(w, 200, authResponse(current, raw, client, req.RequestUser, true))
}
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decode(w, r, &req) {
		return
	}
	// Every token is already bound to the sole profile; selecting a profile is invalid.
	if req.SelectedProfile != nil {
		apiError(w, 400, "IllegalArgumentException", "This token already has a selected profile.")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var uid, client string
	err = tx.QueryRowContext(r.Context(), queries.ConsumeToken, digest(req.AccessToken), time.Now().Unix(), req.ClientToken, req.ClientToken).
		Scan(&uid, &client)
	if errors.Is(err, sql.ErrNoRows) {
		forbidden(w)
		return
	}
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	u, err := scanUser(tx.QueryRowContext(r.Context(), queries.GetUserByID, uid))
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	raw := randomHex()
	if err = insertToken(r.Context(), tx, uid, client, raw); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	jsonResponse(w, 200, authResponse(u, raw, client, req.RequestUser, false))
}
func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decode(w, r, &req) {
		return
	}
	_, err := s.lookupToken(r.Context(), req.AccessToken, req.ClientToken, false)
	if errors.Is(err, sql.ErrNoRows) {
		forbidden(w)
		return
	}
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) invalidate(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decode(w, r, &req) {
		return
	}
	if _, err := queries.New(s.db).DeleteToken(r.Context(), digest(req.AccessToken)); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) signout(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if !decode(w, r, &req) {
		return
	}
	u, ok := s.credentials(w, r, req)
	if !ok {
		return
	}
	// A concurrent key rotation revokes all earlier tokens itself.
	if _, err := queries.New(s.db).
		SignOut(r.Context(), queries.SignOutParams{UserID: u.ID, ID: u.ID, SecretHash: u.SecretHash}); err != nil {
		internalError(r.Context(), w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccessToken     string `json:"accessToken"`
		SelectedProfile string `json:"selectedProfile"`
		ServerID        string `json:"serverId"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.ServerID == "" || len(req.ServerID) > 128 {
		apiError(w, 400, "IllegalArgumentException", "Invalid serverId.")
		return
	}
	// Insert from a valid token in one statement, avoiding revocation races.
	result, err := queries.New(s.db).
		JoinServer(r.Context(), queries.JoinServerParams{ServerID: req.ServerID, Ip: s.clientIP(r), Expires: time.Now().Add(30 * time.Second).Unix(), Hash: digest(req.AccessToken), UserID: req.SelectedProfile, Expires_2: time.Now().Unix()})
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	if n == 0 {
		forbidden(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) hasJoined(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ip := q.Get("ip")
	if ip != "" {
		parsed, err := netip.ParseAddr(ip)
		if err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		ip = parsed.Unmap().String()
	}
	var uid string
	err := s.db.QueryRowContext(r.Context(), queries.FindJoinedUser, q.Get("serverId"), q.Get("username"), time.Now().Unix(), time.Now().Unix(), ip, ip).
		Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	u, err := s.userByID(r.Context(), uid)
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	s.writeProfile(r.Context(), w, u, true)
}
func (s *Server) writeProfile(ctx context.Context, w http.ResponseWriter, u user, signed bool) {
	p, err := s.fullProfile(u, signed)
	if err != nil {
		internalError(ctx, w, err)
		return
	}
	jsonResponse(w, 200, p)
}
func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	u, err := s.userByID(r.Context(), r.PathValue("uuid"))
	if errors.Is(err, sql.ErrNoRows) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	s.writeProfile(r.Context(), w, u, r.URL.Query().Get("unsigned") == "false")
}
func (s *Server) profileByName(w http.ResponseWriter, r *http.Request) {
	u, err := s.userByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, sql.ErrNoRows) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	jsonResponse(w, 200, basicProfile(u))
}
func (s *Server) profiles(w http.ResponseWriter, r *http.Request) {
	var names []string
	if !decode(w, r, &names) {
		return
	}
	if names == nil || len(names) > 100 {
		apiError(w, 400, "IllegalArgumentException", "Expected an array of at most 100 names.")
		return
	}
	result := []profile{}
	seen := map[string]bool{}
	for _, name := range names {
		u, err := s.userByName(r.Context(), name)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			internalError(r.Context(), w, err)
			return
		}
		if !seen[u.ID] {
			result = append(result, basicProfile(u))
			seen[u.ID] = true
		}
	}
	jsonResponse(w, 200, result)
}
