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
}
type ImportResult struct{ Added, Unchanged int }

var importUUIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ImportProfiles is insert-only and atomic. A dry run never creates or writes the
// target database. Repeating an identical import preserves credentials and sessions.
func ImportProfiles(ctx context.Context, dir string, profiles []ImportedProfile, apply bool) (ImportResult, error) {
	result := ImportResult{}
	if dir == "" {
		return result, errors.New("target data directory is required")
	}
	if len(profiles) == 0 {
		return result, errors.New("no profiles selected for import")
	}
	hashes := make([]string, len(profiles))
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
		if p.PNG != nil {
			normalized, err := skin.Normalize(bytes.NewReader(p.PNG), p.Model)
			if err != nil {
				return result, fmt.Errorf("profile %d: %w", i+1, err)
			}
			profiles[i].PNG = normalized
			hashes[i] = digest(string(normalized))
		}
	}
	var db *sql.DB
	var err error
	if apply {
		db, err = openDB(dir)
	} else {
		path, e := filepath.Abs(filepath.Join(dir, "atto.db"))
		if e != nil {
			return result, e
		}
		_, e = os.Stat(path)
		if errors.Is(e, os.ErrNotExist) {
			return ImportResult{Added: len(profiles)}, nil
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
		rows, err := tx.QueryContext(ctx, "SELECT id,issuer,subject,name,skin_hash,model FROM users WHERE id=? OR (issuer=? AND subject=?) OR name=? COLLATE NOCASE", p.ID, p.Issuer, p.Subject, p.Name)
		if err != nil {
			return ImportResult{}, err
		}
		matches := 0
		identical := true
		for rows.Next() {
			var id, issuer, sub, name, hash, model string
			if err = rows.Scan(&id, &issuer, &sub, &name, &hash, &model); err != nil {
				rows.Close()
				return ImportResult{}, err
			}
			matches++
			identical = identical && id == p.ID && issuer == p.Issuer && sub == p.Subject && name == p.Name && hash == hashes[i] && model == p.Model
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
			if hashes[i] != "" {
				var png []byte
				if err = tx.QueryRowContext(ctx, "SELECT png FROM textures WHERE hash=?", hashes[i]).Scan(&png); err != nil || !bytes.Equal(png, p.PNG) {
					return ImportResult{}, fmt.Errorf("existing profile %q has missing or inconsistent texture data", p.Name)
				}
			}
			result.Unchanged++
			continue
		}
		result.Added++
		if !apply {
			continue
		}
		if p.PNG != nil {
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO textures(hash,png) VALUES(?,?)", hashes[i], p.PNG); err != nil {
				return ImportResult{}, err
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO users(id,issuer,subject,name,skin_hash,model) VALUES(?,?,?,?,?,?)", p.ID, p.Issuer, p.Subject, p.Name, hashes[i], p.Model)
		if err != nil {
			return ImportResult{}, err
		}
		if hashes[i] != "" {
			if err = rememberSkin(tx, p.ID, hashes[i], p.Model); err != nil {
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
