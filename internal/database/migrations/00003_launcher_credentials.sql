-- +goose Up
ALTER TABLE users ADD COLUMN launcher_secret BLOB;
UPDATE users SET secret_hash='' WHERE secret_hash<>'';
DELETE FROM tokens WHERE expires <= unixepoch();
ALTER TABLE tokens DROP COLUMN expires;
PRAGMA user_version=4;

-- +goose Down
ALTER TABLE tokens ADD COLUMN expires INTEGER NOT NULL DEFAULT 0;
UPDATE tokens SET expires=issued + 1296000;
ALTER TABLE users DROP COLUMN launcher_secret;
PRAGMA user_version=3;
