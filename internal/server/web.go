package server

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
)

type pageData struct {
	LoggedIn                            bool
	User                                user
	CSRF, BaseURL, Message, LauncherKey string
	HasKey                              bool
	History                             []historySkin
	HistoryPage, PreviousPage, NextPage int
	HasPrevious, HasNext                bool
}

func (s *Server) render(w http.ResponseWriter, v session, message, key string, status int) {
	s.renderPage(w, v, message, key, status, 0)
}
func (s *Server) renderPage(w http.ResponseWriter, v session, message, key string, status, page int) {
	data := pageData{LoggedIn: v.User.ID != "", User: v.User, CSRF: v.CSRF, BaseURL: s.cfg.BaseURL, Message: message, LauncherKey: key, HasKey: v.User.SecretHash != ""}
	if data.LoggedIn {
		var err error
		data.History, data.HasNext, err = s.skinHistory(v.User, page)
		if err != nil {
			internalError(w, err)
			return
		}
		data.HistoryPage, data.PreviousPage, data.NextPage = page+1, page-1, page+1
		data.HasPrevious = page > 0
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.page.Execute(w, data); err != nil {
		slog.Error("render page", "error", err)
	}
}
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	v, err := s.session(r)
	if err != nil && err != sql.ErrNoRows && err != http.ErrNoCookie {
		internalError(w, err)
		return
	}
	message := map[string]string{"profile": "角色名已保存。请在启动器中刷新登录。", "skin": "皮肤已保存。重新进入服务器即可加载。", "removed": "皮肤已移除，将使用默认外观。"}[r.URL.Query().Get("saved")]
	page := 0
	if raw := r.URL.Query().Get("history_page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 0 || page > 1000000 {
			http.Error(w, "页码无效。", 400)
			return
		}
	}
	s.renderPage(w, v, message, "", 200, page)
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	types := map[string]string{"style.css": "text/css; charset=utf-8", "app.js": "text/javascript; charset=utf-8"}
	contentType, ok := types[file]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := webFiles.ReadFile("web/" + file)
	if err != nil {
		internalError(w, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(data)
}
