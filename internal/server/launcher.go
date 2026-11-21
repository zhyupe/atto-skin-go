package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/zhyupe/atto-skin-go/internal/database/queries"
)

func newLauncherCipher(ctx context.Context, db *sql.DB) (cipher.AEAD, error) {
	key, err := loadSecretKey(ctx, db, "launcher_encryption_key", 32)
	if err != nil {
		return nil, fmt.Errorf("initialize launcher cipher: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize launcher cipher: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("initialize launcher cipher: %w", err)
	}
	return aead, nil
}

func (s *Server) saveLauncherSecret(ctx context.Context, q *queries.Queries, uid, raw string) error {
	//nolint:gosec // NewGCMWithRandomNonce generates and prefixes the nonce.
	sealed := s.launcherCipher.Seal(nil, nil, []byte(raw), []byte(uid))
	_, err := q.SetLauncherSecret(ctx, queries.SetLauncherSecretParams{
		ID:             uid,
		SecretHash:     digest(raw),
		LauncherSecret: sealed,
	})
	if err != nil {
		return fmt.Errorf("save launcher secret: %w", err)
	}
	return nil
}

func (s *Server) viewLauncherKey(w http.ResponseWriter, r *http.Request, v session) {
	ctx := r.Context()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		internalError(ctx, w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	q := queries.New(tx)
	sealed, err := q.GetLauncherSecret(ctx, v.User.ID)
	if err != nil {
		internalError(ctx, w, err)
		return
	}
	var key string
	if len(sealed) != 0 {
		plain, e := s.launcherCipher.Open(nil, nil, sealed, []byte(v.User.ID))
		if e != nil {
			internalError(ctx, w, e)
			return
		}
		key = string(plain)
	} else {
		key = randomHex()
		if err = s.saveLauncherSecret(ctx, q, v.User.ID, key); err != nil {
			internalError(ctx, w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		internalError(ctx, w, err)
		return
	}
	v.User.SecretHash = digest(key)
	s.render(ctx, w, v, "", key, http.StatusOK)
}

func (s *Server) rotateKey(w http.ResponseWriter, r *http.Request, v session) {
	ctx := r.Context()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		internalError(ctx, w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	q := queries.New(tx)
	if err = s.saveLauncherSecret(ctx, q, v.User.ID, randomHex()); err != nil {
		internalError(ctx, w, err)
		return
	}
	if _, err = q.DeleteUserTokens(ctx, v.User.ID); err != nil {
		internalError(ctx, w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(ctx, w, err)
		return
	}
	http.Redirect(w, r, "/?saved=launcher-key#connect-panel", http.StatusSeeOther)
}
