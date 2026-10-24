package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/zhyupe/atto-skin-go/internal/database"
	"github.com/zhyupe/atto-skin-go/internal/database/queries"
)

type user struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SecretHash string `json:"-"`
	SkinHash   string `json:"skinHash"`
	Model      string `json:"model"`
}

func randomHex() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return hex.EncodeToString(b)
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func openDB(ctx context.Context, dir string) (*sql.DB, error) {
	db, err := database.Open(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("open application database: %w", err)
	}
	return db, nil
}

func loadKey(ctx context.Context, db *sql.DB) (*rsa.PrivateKey, error) {
	var der []byte
	err := db.QueryRowContext(ctx, queries.GetSigningKey).Scan(&der)
	if errors.Is(err, sql.ErrNoRows) {
		key, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			return nil, fmt.Errorf("load key: %w", e)
		}
		der = x509.MarshalPKCS1PrivateKey(key)
		if _, e = queries.New(db).PutSigningKey(ctx, der); e != nil {
			return nil, fmt.Errorf("load key: %w", e)
		}
		err = db.QueryRowContext(ctx, queries.GetSigningKey).Scan(&der)
	}
	if err != nil {
		return nil, fmt.Errorf("load key: %w", err)
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse signing key: %w", err)
	}
	return key, nil
}
func publicKey(key *rsa.PrivateKey) string {
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func scanUser(row interface{ Scan(...any) error }) (user, error) {
	var u user
	err := row.Scan(&u.ID, &u.Name, &u.SecretHash, &u.SkinHash, &u.Model)
	if err != nil {
		return u, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}
func (s *Server) userByID(ctx context.Context, id string) (user, error) {
	row, err := queries.New(s.db).GetUserByID(ctx, id)
	if err != nil {
		return user{}, fmt.Errorf("get user by id: %w", err)
	}
	return user(row), nil
}
func (s *Server) userByName(ctx context.Context, name string) (user, error) {
	row, err := queries.New(s.db).GetUserByName(ctx, name)
	if err != nil {
		return user{}, fmt.Errorf("get user by name: %w", err)
	}
	return user(row), nil
}
func (s *Server) ensureUser(ctx context.Context, issuer, subject string) (user, error) {
	id := newUUID()
	// Stable identity is the issuer/subject pair, never an email address or display name.
	_, err := queries.New(s.db).
		EnsureUser(ctx, queries.EnsureUserParams{ID: id, Issuer: issuer, Subject: subject, Name: "p_" + id[:14]})
	if err != nil {
		return user{}, fmt.Errorf("ensure user: %w", err)
	}
	return scanUser(s.db.QueryRowContext(ctx, queries.GetUserByIdentity, issuer, subject))
}
func (s *Server) cleanup(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, query := range []string{queries.ExpireJoins, queries.ExpireTokens, queries.ExpireWebSessions, queries.ExpireOidcFlows} {
		if _, err = tx.ExecContext(ctx, query, time.Now().Unix()); err != nil {
			return fmt.Errorf("cleanup: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("cleanup: %w", err)
	}
	return nil
}
