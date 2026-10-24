-- +goose Up

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

-- +goose Down
SELECT 1;
