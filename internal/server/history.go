package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/zhyupe/atto-skin-go/internal/database/queries"
)

const historyPageSize = 12

type historySkin struct {
	Hash, Model string
	Current     bool
	Number      int
}

func rememberSkin(ctx context.Context, tx *sql.Tx, uid, hash, model string) error {
	_, err := queries.New(tx).
		RememberSkin(ctx, queries.RememberSkinParams{UserID: uid, SkinHash: hash, Model: model, LastUsed: time.Now().UnixNano()})
	if err != nil {
		return fmt.Errorf("remember skin: %w", err)
	}
	return nil
}

func (s *Server) skinHistory(ctx context.Context, u user, page int) ([]historySkin, bool, error) {
	rows, err := s.db.QueryContext(ctx, queries.ListSkinHistory, u.ID, historyPageSize+1, page*historyPageSize)
	if err != nil {
		return nil, false, fmt.Errorf("skin history: %w", err)
	}
	defer rows.Close()
	skins := []historySkin{}
	for rows.Next() {
		var item historySkin
		if err = rows.Scan(&item.Hash, &item.Model); err != nil {
			return nil, false, fmt.Errorf("skin history: %w", err)
		}
		item.Number = page*historyPageSize + len(skins) + 1
		item.Current = item.Hash == u.SkinHash && item.Model == u.Model
		skins = append(skins, item)
	}
	if err = rows.Err(); err != nil {
		return nil, false, fmt.Errorf("skin history: %w", err)
	}
	more := len(skins) > historyPageSize
	if more {
		skins = skins[:historyPageSize]
	}
	return skins, more, nil
}

func (s *Server) restoreSkin(ctx context.Context, uid, hash, model string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("restore skin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// A public texture URL is not proof that this account has used the skin.
	var exists int
	if err = tx.QueryRowContext(ctx, queries.RequireHistorySkin, uid, hash, model).Scan(&exists); err != nil {
		return fmt.Errorf("restore skin: %w", err)
	}
	if _, err = queries.New(tx).
		SetUserSkin(ctx, queries.SetUserSkinParams{SkinHash: hash, Model: model, ID: uid}); err != nil {
		return fmt.Errorf("restore skin: %w", err)
	}
	if err = rememberSkin(ctx, tx, uid, hash, model); err != nil {
		return fmt.Errorf("restore skin: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("restore skin: %w", err)
	}
	return nil
}

func (s *Server) restoreWeb(w http.ResponseWriter, r *http.Request, v session) {
	hash, model := r.PostFormValue("hash"), r.PostFormValue("model")
	if !hashPattern.MatchString(hash) || (model != defaultModel && model != slimModel) {
		http.Error(w, "皮肤记录无效。", http.StatusBadRequest)
		return
	}
	if !s.allow("upload:"+v.User.ID, 20) {
		http.Error(w, "切换过于频繁，请稍后重试。", http.StatusTooManyRequests)
		return
	}
	if err := s.restoreSkin(r.Context(), v.User.ID, hash, model); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "找不到这条皮肤记录。", http.StatusNotFound)
		return
	} else if err != nil {
		internalError(r.Context(), w, err)
		return
	}
	http.Redirect(w, r, "/?saved=skin#skin-history", http.StatusSeeOther)
}
