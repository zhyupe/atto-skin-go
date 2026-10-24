-- name: DeleteOIDCFlow :execresult
DELETE FROM oidc_flows WHERE hash=?;

-- name: CreateOIDCFlow :execresult
INSERT INTO oidc_flows(hash,state,nonce,verifier,expires) VALUES(?,?,?,?,?);

-- name: ConsumeOIDCFlow :one
DELETE FROM oidc_flows WHERE hash=? RETURNING state,nonce,verifier,expires;

-- name: DeleteLegacySession :execresult
DELETE FROM web_sessions WHERE hash=?;

-- name: CreateLegacySession :execresult
INSERT INTO web_sessions(hash,user_id,csrf,expires) VALUES(?,?,?,?);

-- name: PruneLegacySessions :execresult
DELETE FROM web_sessions WHERE web_sessions.user_id=? AND web_sessions.rowid NOT IN (SELECT web_sessions.rowid FROM web_sessions WHERE web_sessions.user_id=? ORDER BY web_sessions.rowid DESC LIMIT 10);

-- name: GetLegacySession :one
SELECT user_id,csrf FROM web_sessions WHERE hash=? AND expires>?;

-- name: FindUserIDByName :one
SELECT id FROM users WHERE name=? COLLATE NOCASE;

-- name: GetUserName :one
SELECT name FROM users WHERE id=?;

-- name: RenameUser :execresult
UPDATE users SET name=? WHERE id=?;

-- name: StaleUserTokens :execresult
UPDATE tokens SET stale=1 WHERE user_id=?;

-- name: DeleteUserJoins :execresult
DELETE FROM joins WHERE user_id=?;

-- name: SetLauncherSecret :execresult
UPDATE users SET secret_hash=? WHERE id=?;

-- name: DeleteUserTokens :execresult
DELETE FROM tokens WHERE user_id=?;

-- name: RememberSkin :execresult
INSERT INTO skin_history(user_id,skin_hash,model,last_used) VALUES(?,?,?,?)
 ON CONFLICT(user_id,skin_hash,model) DO UPDATE SET last_used=excluded.last_used;

-- name: ListSkinHistory :many
SELECT skin_hash,model FROM skin_history WHERE user_id=?
 ORDER BY last_used DESC,skin_hash,model LIMIT ? OFFSET ?;

-- name: RequireHistorySkin :one
SELECT 1 FROM skin_history WHERE user_id=? AND skin_hash=? AND model=?;

-- name: SetUserSkin :execresult
UPDATE users SET skin_hash=?,model=? WHERE id=?;

-- name: GetTexture :one
SELECT png FROM textures WHERE hash=?;

-- name: PutTexture :execresult
INSERT OR IGNORE INTO textures(hash,png) VALUES(?,?);

-- name: ImportUser :execresult
INSERT INTO users(id,issuer,subject,name,skin_hash,model) VALUES(?,?,?,?,?,?);

-- name: HasHistorySkin :one
SELECT EXISTS(SELECT 1 FROM skin_history WHERE user_id=? AND skin_hash=? AND model=?);

-- name: ImportHistorySkin :execresult
INSERT INTO skin_history(user_id,skin_hash,model,last_used) VALUES(?,?,?,0);

-- name: RequireSkinToken :one
SELECT user_id FROM tokens WHERE hash=? AND user_id=? AND expires>? AND stale=0;

-- name: ArchiveCurrentSkin :execresult
INSERT OR IGNORE INTO skin_history(user_id,skin_hash,model,last_used)
 SELECT id,skin_hash,model,0 FROM users WHERE id=? AND skin_hash != '';

-- name: GetSigningKey :one
SELECT value FROM settings WHERE key='signing_key';

-- name: PutSigningKey :execresult
INSERT OR IGNORE INTO settings(key,value) VALUES('signing_key',?);

-- name: GetUserByID :one
SELECT id,name,secret_hash,skin_hash,model FROM users WHERE id=?;

-- name: GetUserByName :one
SELECT id,name,secret_hash,skin_hash,model FROM users WHERE name=? COLLATE NOCASE;

-- name: EnsureUser :execresult
INSERT INTO users(id,issuer,subject,name) VALUES(?,?,?,?) ON CONFLICT(issuer,subject) DO NOTHING;

-- name: GetUserByIdentity :one
SELECT id,name,secret_hash,skin_hash,model FROM users WHERE issuer=? AND subject=?;

-- name: CountSkinHistory :one
SELECT count(*) FROM skin_history WHERE user_id=?;

-- name: LookupToken :one
SELECT user_id,client,stale FROM tokens WHERE hash=? AND expires>?;

-- name: CreateToken :execresult
INSERT INTO tokens(hash,user_id,client,issued,expires) VALUES(?,?,?,?,?);

-- name: PruneTokens :execresult
DELETE FROM tokens WHERE tokens.user_id=? AND tokens.rowid NOT IN (SELECT tokens.rowid FROM tokens WHERE tokens.user_id=? ORDER BY tokens.rowid DESC LIMIT 10);

-- name: GetUserBySecret :one
SELECT id,name,secret_hash,skin_hash,model FROM users WHERE id=? AND secret_hash=?;

-- name: ConsumeToken :one
DELETE FROM tokens WHERE hash=? AND expires>? AND (? IS NULL OR client=?) RETURNING user_id,client;

-- name: DeleteToken :execresult
DELETE FROM tokens WHERE hash=?;

-- name: SignOut :execresult
DELETE FROM tokens WHERE tokens.user_id=? AND EXISTS(SELECT 1 FROM users WHERE id=? AND secret_hash=?);

-- name: JoinServer :execresult
INSERT INTO joins(server_id,user_id,token_hash,ip,expires)
 SELECT ?,tokens.user_id,tokens.hash,?,? FROM tokens WHERE tokens.hash=? AND tokens.user_id=? AND tokens.expires>? AND tokens.stale=0
 ON CONFLICT(server_id,user_id) DO UPDATE SET token_hash=excluded.token_hash,ip=excluded.ip,expires=excluded.expires;

-- name: FindJoinedUser :one
SELECT u.id FROM joins j JOIN users u ON u.id=j.user_id JOIN tokens t ON t.hash=j.token_hash
 WHERE j.server_id=? AND u.name=? COLLATE NOCASE AND j.expires>? AND t.expires>? AND t.stale=0 AND (?='' OR j.ip=?);

-- name: MatchImportUser :many
SELECT id,issuer,subject,name,skin_hash,model FROM users WHERE id=? OR (issuer=? AND subject=?) OR (? AND name=? COLLATE NOCASE);

-- name: GetSetting :one
SELECT value FROM settings WHERE key=?;

-- name: PutSetting :execresult
INSERT OR IGNORE INTO settings(key,value) VALUES(?,?);

-- name: GetSessionVersion :one
SELECT session_version FROM users WHERE id=?;

-- name: RevokeUserCookies :execresult
UPDATE users SET session_version=session_version+1 WHERE id=?;

-- name: ExpireJoins :execresult
DELETE FROM joins WHERE expires<=?;

-- name: ExpireTokens :execresult
DELETE FROM tokens WHERE expires<=?;

-- name: ExpireWebSessions :execresult
DELETE FROM web_sessions WHERE expires<=?;

-- name: ExpireOidcFlows :execresult
DELETE FROM oidc_flows WHERE expires<=?;
