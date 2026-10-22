package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zhyupe/atto-skin-go/internal/skin"
)

// ImportedProfile contains the identity explicitly approved by the operator.
// Imported accounts have no launcher credentials until the owner logs in with OIDC.
type ImportedProfile struct {
	ID, Issuer, Subject, Name, Model string
	PNG                              []byte
	History                          []ImportedSkin
}

// ImportedSkin is a library entry belonging to the explicitly mapped uploader.
type ImportedSkin struct {
	Model string
	PNG   []byte
}
type importedTexture struct {
	ImportedSkin
	hash string
}
type ImportResult struct{ Added, Unchanged, HistoryAdded, HistoryUnchanged int }

var importUUIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ImportProfiles is insert-only and atomic. A dry run never creates or writes the
// target database. Repeating an identical import preserves credentials and sessions.
func ImportProfiles(ctx context.Context, dir string, profiles []ImportedProfile, apply bool) (ImportResult, error) {
	return importProfiles(ctx, dir, profiles, apply, false)
}

// ImportProfileHistory appends library entries to existing, exactly matched UUID/OIDC
// identities, preserving their current name, skin, credentials and history recency.
func ImportProfileHistory(ctx context.Context, dir string, profiles []ImportedProfile, apply bool) (ImportResult, error) {
	return importProfiles(ctx, dir, profiles, apply, true)
}

func importProfiles(ctx context.Context, dir string, profiles []ImportedProfile, apply, historyOnly bool) (ImportResult, error) {
	result := ImportResult{}
	if dir == "" {
		return result, errors.New("target data directory is required")
	}
	if len(profiles) == 0 {
		return result, errors.New("no profiles selected for import")
	}
	hashes := make([]string, len(profiles))
	histories := make([][]importedTexture, len(profiles))
	seenID, seenName, seenIdentity := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, p := range profiles {
		if !importUUIDPattern.MatchString(p.ID) || p.ID == strings.Repeat("0", 32) || !namePattern.MatchString(p.Name) || p.Subject == "" || strings.TrimSpace(p.Subject) != p.Subject {
			return result, fmt.Errorf("invalid identity for profile %d", i+1)
		}
		cfg := Config{BaseURL: "http://localhost", Issuer: p.Issuer, ClientID: "migration", DataDir: dir}
		if err := cfg.Validate(); err != nil {
			return result, err
		}
		identity := p.Issuer + "\x00" + p.Subject
		if strings.ContainsRune(p.Subject, 0) || seenID[p.ID] || seenName[strings.ToLower(p.Name)] || seenIdentity[identity] {
			return result, fmt.Errorf("duplicate UUID, name or OIDC identity for profile %d", i+1)
		}
		seenID[p.ID] = true
		seenName[strings.ToLower(p.Name)] = true
		seenIdentity[identity] = true
		if p.Model != "default" && p.Model != "slim" {
			return result, fmt.Errorf("invalid skin model for profile %d", i+1)
		}
		if !historyOnly && p.PNG != nil {
			normalized, err := skin.Normalize(bytes.NewReader(p.PNG), p.Model)
			if err != nil {
				return result, fmt.Errorf("profile %d: %w", i+1, err)
			}
			profiles[i].PNG = normalized
			hashes[i] = digest(string(normalized))
		}
		seen := map[string]bool{}
		for _, entry := range p.History {
			if entry.Model != "default" && entry.Model != "slim" {
				return result, fmt.Errorf("profile %d: invalid history model", i+1)
			}
			data, err := skin.Normalize(bytes.NewReader(entry.PNG), entry.Model)
			if err != nil {
				return result, fmt.Errorf("profile %d history: %w", i+1, err)
			}
			hash := digest(string(data))
			key := hash + ":" + entry.Model
			if !seen[key] {
				histories[i] = append(histories[i], importedTexture{ImportedSkin: ImportedSkin{Model: entry.Model, PNG: data}, hash: hash})
				seen[key] = true
			}
		}
	}
	var db *sql.DB
	var err error
	if historyOnly {
		if _, err := os.Stat(filepath.Join(dir, "atto.db")); err != nil {
			return result, fmt.Errorf("history-only requires an existing Atto database: %w", err)
		}
	}
	if apply {
		db, err = openDB(dir)
	} else {
		path, e := filepath.Abs(filepath.Join(dir, "atto.db"))
		if e != nil {
			return result, e
		}
		_, e = os.Stat(path)
		if errors.Is(e, os.ErrNotExist) {
			result.Added = len(profiles)
			for i, entries := range histories {
				for _, entry := range entries {
					if entry.hash == hashes[i] && entry.Model == profiles[i].Model {
						result.HistoryUnchanged++
					} else {
						result.HistoryAdded++
					}
				}
			}
			return result, nil
		}
		if e != nil {
			return result, e
		}
		u := url.URL{Scheme: "file", Path: path}
		q := u.Query()
		q.Set("mode", "ro")
		u.RawQuery = q.Encode()
		db, err = sql.Open("sqlite", u.String())
		if err == nil {
			db.SetMaxOpenConns(1)
		}
	}
	if err != nil {
		return result, err
	}
	defer db.Close()
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return result, err
	}
	if version != 1 && version != 2 {
		return result, fmt.Errorf("unsupported target schema version %d", version)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	for i, p := range profiles {
		query := "SELECT id,issuer,subject,name,skin_hash,model FROM users WHERE id=? OR (issuer=? AND subject=?)"
		args := []any{p.ID, p.Issuer, p.Subject}
		if !historyOnly {
			query += " OR name=? COLLATE NOCASE"
			args = append(args, p.Name)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return ImportResult{}, err
		}
		matches, identical := 0, true
		var currentHash, currentModel string
		for rows.Next() {
			var id, issuer, sub, name string
			if err = rows.Scan(&id, &issuer, &sub, &name, &currentHash, &currentModel); err != nil {
				rows.Close()
				return ImportResult{}, err
			}
			matches++
			identical = identical && id == p.ID && issuer == p.Issuer && sub == p.Subject
			if !historyOnly {
				identical = identical && name == p.Name && currentHash == hashes[i] && currentModel == p.Model
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return ImportResult{}, err
		}
		if matches > 0 {
			if matches != 1 || !identical {
				return ImportResult{}, fmt.Errorf("target conflict for profile %q: UUID, name or OIDC account is already in use; no profiles were committed", p.Name)
			}
			if !historyOnly && hashes[i] != "" {
				var png []byte
				if err = tx.QueryRowContext(ctx, "SELECT png FROM textures WHERE hash=?", hashes[i]).Scan(&png); err != nil || !bytes.Equal(png, p.PNG) {
					return ImportResult{}, fmt.Errorf("existing profile %q has missing or inconsistent texture data", p.Name)
				}
			}
			result.Unchanged++
		} else {
			if historyOnly {
				return ImportResult{}, fmt.Errorf("history-only: profile %q has not been migrated; no history was committed", p.Name)
			}
			result.Added++
			currentHash, currentModel = hashes[i], p.Model
			if apply {
				if p.PNG != nil {
					if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO textures(hash,png) VALUES(?,?)", hashes[i], p.PNG); err != nil {
						return ImportResult{}, err
					}
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO users(id,issuer,subject,name,skin_hash,model) VALUES(?,?,?,?,?,?)", p.ID, p.Issuer, p.Subject, p.Name, hashes[i], p.Model); err != nil {
					return ImportResult{}, err
				}
				if hashes[i] != "" {
					if err = rememberSkin(tx, p.ID, hashes[i], p.Model); err != nil {
						return ImportResult{}, err
					}
				}
			}
		}
		for _, entry := range histories[i] {
			// Verify shared blobs even during a dry run. Never mask inconsistent target data.
			var stored []byte
			err = tx.QueryRowContext(ctx, "SELECT png FROM textures WHERE hash=?", entry.hash).Scan(&stored)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return ImportResult{}, err
			}
			if err == nil && !bytes.Equal(stored, entry.PNG) {
				return ImportResult{}, fmt.Errorf("inconsistent target history texture for profile %q", p.Name)
			}
			exists := entry.hash == currentHash && entry.Model == currentModel
			if version == 2 && (matches > 0 || apply) {
				if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM skin_history WHERE user_id=? AND skin_hash=? AND model=?)", p.ID, entry.hash, entry.Model).Scan(&exists); err != nil {
					return ImportResult{}, err
				}
			}
			if exists {
				result.HistoryUnchanged++
				continue
			}
			result.HistoryAdded++
			if !apply {
				continue
			}
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO textures(hash,png) VALUES(?,?)", entry.hash, entry.PNG); err != nil {
				return ImportResult{}, err
			}
			// Importing a library is not wearing it: preserve existing last_used values.
			if _, err = tx.ExecContext(ctx, "INSERT INTO skin_history(user_id,skin_hash,model,last_used) VALUES(?,?,?,0)", p.ID, entry.hash, entry.Model); err != nil {
				return ImportResult{}, err
			}
		}
	}

	if apply {
		if err = tx.Commit(); err != nil {
			return ImportResult{}, err
		}
	}
	return result, nil
}
