package server

import (
	"database/sql"
	"net/http"
	"time"
)

const historyPageSize = 12

type historySkin struct {
	Hash, Model string
	Current     bool
	Number      int
}

func rememberSkin(tx *sql.Tx, uid, hash, model string) error {
	_, err := tx.Exec(`INSERT INTO skin_history(user_id,skin_hash,model,last_used) VALUES(?,?,?,?)
 ON CONFLICT(user_id,skin_hash,model) DO UPDATE SET last_used=excluded.last_used`, uid, hash, model, time.Now().UnixNano())
	return err
}

func (s *Server) skinHistory(u user, page int) ([]historySkin, bool, error) {
	rows, err := s.db.Query(`SELECT skin_hash,model FROM skin_history WHERE user_id=?
 ORDER BY last_used DESC,skin_hash,model LIMIT ? OFFSET ?`, u.ID, historyPageSize+1, page*historyPageSize)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	skins := []historySkin{}
	for rows.Next() {
		var item historySkin
		if err = rows.Scan(&item.Hash, &item.Model); err != nil {
			return nil, false, err
		}
		item.Number = page*historyPageSize + len(skins) + 1
		item.Current = item.Hash == u.SkinHash && item.Model == u.Model
		skins = append(skins, item)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(skins) > historyPageSize
	if more {
		skins = skins[:historyPageSize]
	}
	return skins, more, nil
}

func (s *Server) restoreSkin(uid, hash, model string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A public texture URL is not proof that this account has used the skin.
	var exists int
	if err = tx.QueryRow("SELECT 1 FROM skin_history WHERE user_id=? AND skin_hash=? AND model=?", uid, hash, model).Scan(&exists); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE users SET skin_hash=?,model=? WHERE id=?", hash, model, uid); err != nil {
		return err
	}
	if err = rememberSkin(tx, uid, hash, model); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) restoreWeb(w http.ResponseWriter, r *http.Request, v session) {
	hash, model := r.PostFormValue("hash"), r.PostFormValue("model")
	if !hashPattern.MatchString(hash) || (model != "default" && model != "slim") {
		http.Error(w, "皮肤记录无效。", http.StatusBadRequest)
		return
	}
	if !s.allow("upload:"+v.User.ID, 20) {
		http.Error(w, "切换过于频繁，请稍后重试。", http.StatusTooManyRequests)
		return
	}
	if err := s.restoreSkin(v.User.ID, hash, model); err == sql.ErrNoRows {
		http.Error(w, "找不到这条皮肤记录。", http.StatusNotFound)
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	http.Redirect(w, r, "/?saved=skin#skin-history", http.StatusSeeOther)
}
