package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/zhyupe/atto-skin-go/internal/database/queries"
	"github.com/zhyupe/atto-skin-go/internal/logging"
)

type pageData struct {
	LoggedIn                            bool
	User                                user
	CSRF, BaseURL, Message, LauncherKey string
	HasKey                              bool
	History                             []historySkin
	HistoryPage, PreviousPage, NextPage int
	HasPrevious, HasNext                bool
	HistoryCount                        int
}

func (s *Server) render(ctx context.Context, w http.ResponseWriter, v session, message, key string, status int) {
	s.renderPage(ctx, w, v, message, key, status, 0)
}

func (s *Server) renderPage(
	ctx context.Context,
	w http.ResponseWriter,
	v session,
	message, key string,
	status, page int,
) {
	data := pageData{
		LoggedIn:    v.User.ID != "",
		User:        v.User,
		CSRF:        v.CSRF,
		BaseURL:     s.cfg.BaseURL,
		Message:     message,
		LauncherKey: key,
		HasKey:      v.User.SecretHash != "",
	}
	if data.LoggedIn {
		var err error
		data.History, data.HasNext, err = s.skinHistory(ctx, v.User, page)
		if err != nil {
			internalError(ctx, w, err)
			return
		}
		data.HistoryPage, data.PreviousPage, data.NextPage = page+1, page-1, page+1
		data.HasPrevious = page > 0
		if err = s.db.QueryRowContext(ctx, queries.CountSkinHistory, v.User.ID).Scan(&data.HistoryCount); err != nil {
			internalError(ctx, w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.page.Execute(w, data); err != nil {
		logging.From(ctx).WithError(err).Error("render page")
	}
}
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	v, err := s.session(r)
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, http.ErrNoCookie) &&
		!errors.Is(err, errNoSession) {
		internalError(r.Context(), w, err)
		return
	}
	message := map[string]string{"profile": "角色名已保存。请在启动器中刷新登录。", skinKind: "皮肤已保存。重新进入服务器即可加载。", "removed": "皮肤已移除，将使用默认外观。"}[r.URL.Query().Get("saved")]
	page := 0
	if raw := r.URL.Query().Get("history_page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 0 || page > 1000000 {
			http.Error(w, "页码无效。", http.StatusBadRequest)
			return
		}
	}
	s.renderPage(r.Context(), w, v, message, "", 200, page)
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	types := map[string]string{
		"style.css":        "text/css; charset=utf-8",
		"app.js":           javascriptType,
		"theme.js":         javascriptType,
		"skin-renderer.js": javascriptType,
	}
	contentType, ok := types[file]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := webFiles.ReadFile("web/" + file)
	if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}
