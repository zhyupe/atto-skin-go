package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
 id TEXT PRIMARY KEY, issuer TEXT NOT NULL, subject TEXT NOT NULL,
 name TEXT NOT NULL UNIQUE COLLATE NOCASE,
 secret_hash TEXT NOT NULL DEFAULT '', skin_hash TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT 'default',
 UNIQUE(issuer,subject)
);
CREATE TABLE IF NOT EXISTS textures (hash TEXT PRIMARY KEY, png BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS web_sessions (
 hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), csrf TEXT NOT NULL, expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS oidc_flows (
 hash TEXT PRIMARY KEY, state TEXT NOT NULL, nonce TEXT NOT NULL, verifier TEXT NOT NULL, expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tokens (
 hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), client TEXT NOT NULL,
 issued INTEGER NOT NULL, expires INTEGER NOT NULL, stale INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS tokens_user ON tokens(user_id);
CREATE TABLE IF NOT EXISTS joins (
 server_id TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), token_hash TEXT NOT NULL REFERENCES tokens(hash) ON DELETE CASCADE,
 ip TEXT NOT NULL, expires INTEGER NOT NULL, PRIMARY KEY(server_id,user_id)
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS skin_history (
 user_id TEXT NOT NULL REFERENCES users(id), skin_hash TEXT NOT NULL REFERENCES textures(hash),
 model TEXT NOT NULL CHECK(model IN ('default','slim')), last_used INTEGER NOT NULL,
 PRIMARY KEY(user_id,skin_hash,model)
);
CREATE INDEX IF NOT EXISTS skin_history_recent ON skin_history(user_id,last_used DESC,skin_hash,model);
INSERT OR IGNORE INTO skin_history(user_id,skin_hash,model,last_used)
 SELECT id,skin_hash,model,0 FROM users WHERE skin_hash != '';
PRAGMA user_version=2;
`

type user struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SecretHash string `json:"-"`
	SkinHash   string `json:"skinHash"`
	Model      string `json:"model"`
}

func randomHex(n int) string {
	b := make([]byte, n)
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

func openDB(dir string) (*sql.DB, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "atto.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err == nil && version > 2 {
		err = fmt.Errorf("database schema %d is newer than this application", version)
	}
	if err == nil {
		_, err = db.Exec("PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;")
	}
	if err == nil {
		var tx *sql.Tx
		tx, err = db.Begin()
		if err == nil {
			if _, err = tx.Exec(schema); err == nil {
				err = tx.Commit()
			}
			tx.Rollback()
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func loadKey(db *sql.DB) (*rsa.PrivateKey, error) {
	var der []byte
	err := db.QueryRow("SELECT value FROM settings WHERE key='signing_key'").Scan(&der)
	if err == sql.ErrNoRows {
		key, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			return nil, e
		}
		der = x509.MarshalPKCS1PrivateKey(key)
		if _, e = db.Exec("INSERT OR IGNORE INTO settings(key,value) VALUES('signing_key',?)", der); e != nil {
			return nil, e
		}
		err = db.QueryRow("SELECT value FROM settings WHERE key='signing_key'").Scan(&der)
	}
	if err != nil {
		return nil, err
	}
	return x509.ParsePKCS1PrivateKey(der)
}
func publicKey(key *rsa.PrivateKey) string {
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

const userColumns = "id,name,secret_hash,skin_hash,model"

func scanUser(row interface{ Scan(...any) error }) (user, error) {
	var u user
	err := row.Scan(&u.ID, &u.Name, &u.SecretHash, &u.SkinHash, &u.Model)
	return u, err
}
func (s *Server) userByID(id string) (user, error) {
	return scanUser(s.db.QueryRow("SELECT "+userColumns+" FROM users WHERE id=?", id))
}
func (s *Server) userByName(name string) (user, error) {
	return scanUser(s.db.QueryRow("SELECT "+userColumns+" FROM users WHERE name=? COLLATE NOCASE", name))
}
func (s *Server) ensureUser(issuer, subject string) (user, error) {
	id := newUUID()
	// Stable identity is the issuer/subject pair, never an email address or display name.
	_, err := s.db.Exec("INSERT INTO users(id,issuer,subject,name) VALUES(?,?,?,?) ON CONFLICT(issuer,subject) DO NOTHING", id, issuer, subject, "p_"+id[:14])
	if err != nil {
		return user{}, err
	}
	return scanUser(s.db.QueryRow("SELECT "+userColumns+" FROM users WHERE issuer=? AND subject=?", issuer, subject))
}
func (s *Server) cleanup() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"joins", "tokens", "web_sessions", "oidc_flows"} {
		if _, err = tx.Exec("DELETE FROM "+table+" WHERE expires<=?", time.Now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
