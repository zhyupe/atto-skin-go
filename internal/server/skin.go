package server

import (
	"bytes"
	"database/sql"
	"errors"
	"image/png"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var errSkin = errors.New("请上传不超过 1 MiB 的 64×64 或 64×32 PNG 皮肤；纤细模型须使用 64×64。")

func readSkin(r *http.Request) ([]byte, string, error) {
	model := r.FormValue("model")
	if model == "" {
		model = "default"
	}
	if model != "default" && model != "slim" {
		return nil, "", errSkin
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		return nil, "", errSkin
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, "", errSkin
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != 64 || (cfg.Height != 32 && cfg.Height != 64) || (model == "slim" && cfg.Height != 64) {
		return nil, "", errSkin
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", errSkin
	}
	var b bytes.Buffer
	if err = png.Encode(&b, img); err != nil {
		return nil, "", err
	}
	return b.Bytes(), model, nil
}
func (s *Server) setSkin(uid string, data []byte, model, tokenHash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if tokenHash != "" {
		var id string
		if err = tx.QueryRow("SELECT user_id FROM tokens WHERE hash=? AND user_id=? AND expires>? AND stale=0", tokenHash, uid, time.Now().Unix()).Scan(&id); err != nil {
			return err
		}
	}
	hash := ""
	if data != nil {
		hash = digest(string(data))
		if _, err = tx.Exec("INSERT OR IGNORE INTO textures(hash,png) VALUES(?,?)", hash, data); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE users SET skin_hash=?,model=? WHERE id=?", hash, model, uid); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Server) uploadWeb(w http.ResponseWriter, r *http.Request, v session) {
	if !s.allow("upload:"+v.User.ID, 20) {
		http.Error(w, "上传过于频繁，请稍后重试。", 429)
		return
	}
	data, model, err := readSkin(r)
	if err != nil {
		s.render(w, v, err.Error(), "", 400)
		return
	}
	if err = s.setSkin(v.User.ID, data, model, ""); err != nil {
		internalError(w, err)
		return
	}
	http.Redirect(w, r, "/?saved=skin", 303)
}
func (s *Server) deleteWeb(w http.ResponseWriter, r *http.Request, v session) {
	if err := s.setSkin(v.User.ID, nil, "default", ""); err != nil {
		internalError(w, err)
		return
	}
	http.Redirect(w, r, "/?saved=removed", 303)
}
func (s *Server) uploadProtocol(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		apiError(w, 401, "Unauthorized", "Bearer token required.")
		return
	}
	t, err := s.lookupToken(strings.TrimPrefix(auth, "Bearer "), nil, false)
	if err == sql.ErrNoRows {
		apiError(w, 401, "Unauthorized", "Invalid token.")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if t.UserID != r.PathValue("uuid") {
		forbidden(w)
		return
	}
	if r.PathValue("kind") != "skin" {
		apiError(w, 400, "IllegalArgumentException", "Only skin textures are supported.")
		return
	}
	if !s.allow("upload:"+t.UserID, 20) {
		apiError(w, 429, "TooManyRequestsException", "Too many uploads.")
		return
	}
	var data []byte
	model := "default"
	if r.Method == http.MethodPut {
		r.Body = http.MaxBytesReader(w, r.Body, (1<<20)+65536)
		if err = r.ParseMultipartForm(1 << 20); err != nil {
			apiError(w, 400, "IllegalArgumentException", "Invalid or oversized multipart form.")
			return
		}
		defer r.MultipartForm.RemoveAll()
		data, model, err = readSkin(r)
		if err != nil {
			apiError(w, 400, "IllegalArgumentException", err.Error())
			return
		}
	}
	if err = s.setSkin(t.UserID, data, model, t.Hash); err == sql.ErrNoRows {
		apiError(w, 401, "Unauthorized", "Invalid token.")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(204)
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Server) texture(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !hashPattern.MatchString(hash) {
		http.NotFound(w, r)
		return
	}
	var data []byte
	err := s.db.QueryRow("SELECT png FROM textures WHERE hash=?", hash).Scan(&data)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+hash+`"`)
	http.ServeContent(w, r, hash, time.Time{}, bytes.NewReader(data))
}
