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

	"github.com/zhyupe/atto-skin-go/internal/database"
	"github.com/zhyupe/atto-skin-go/internal/database/queries"
	"github.com/zhyupe/atto-skin-go/internal/validation"

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
func ImportProfileHistory(
	ctx context.Context,
	dir string,
	profiles []ImportedProfile,
	apply bool,
) (ImportResult, error) {
	return importProfiles(ctx, dir, profiles, apply, true)
}

type preparedProfile struct {
	profile ImportedProfile
	hash    string
	history []importedTexture
}

type importMode struct {
	apply, historyOnly bool
	version            int
}

func importProfiles(
	ctx context.Context,
	dir string,
	profiles []ImportedProfile,
	apply, historyOnly bool,
) (ImportResult, error) {
	if dir == "" {
		return ImportResult{}, errors.New("target data directory is required")
	}
	if len(profiles) == 0 {
		return ImportResult{}, errors.New("no profiles selected for import")
	}
	prepared, err := prepareProfiles(profiles, dir, historyOnly)
	if err != nil {
		return ImportResult{}, err
	}
	db, err := openImportDatabase(ctx, dir, apply, historyOnly)
	if errors.Is(err, os.ErrNotExist) && !apply && !historyOnly {
		return emptyImportResult(prepared), nil
	}
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = db.Close() }()
	var version int
	if err = db.QueryRowContext(ctx, database.SchemaVersion).Scan(&version); err != nil {
		return ImportResult{}, fmt.Errorf("read target schema: %w", err)
	}
	if version < 1 || version > database.Version {
		return ImportResult{}, fmt.Errorf("unsupported target schema version %d", version)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, fmt.Errorf("begin import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result := ImportResult{}
	mode := importMode{apply: apply, historyOnly: historyOnly, version: version}
	for _, p := range prepared {
		if err = importRecord(ctx, tx, p, mode, &result); err != nil {
			return ImportResult{}, err
		}
	}
	if apply {
		if err = tx.Commit(); err != nil {
			return ImportResult{}, fmt.Errorf("commit import: %w", err)
		}
	}
	return result, nil
}

func prepareProfiles(profiles []ImportedProfile, dir string, historyOnly bool) ([]preparedProfile, error) {
	result := make([]preparedProfile, 0, len(profiles))
	ids, names, identities := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, p := range profiles {
		if !importUUIDPattern.MatchString(p.ID) || p.ID == strings.Repeat("0", 32) || !validation.PlayerName(p.Name) ||
			p.Subject == "" ||
			strings.TrimSpace(p.Subject) != p.Subject {
			return nil, fmt.Errorf("invalid identity for profile %d", i+1)
		}
		cfg := Config{BaseURL: "http://localhost", Issuer: p.Issuer, ClientID: "migration", DataDir: dir}
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		identity := p.Issuer + "\x00" + p.Subject
		if strings.ContainsRune(p.Subject, 0) || ids[p.ID] || names[strings.ToLower(p.Name)] || identities[identity] {
			return nil, fmt.Errorf("duplicate UUID, name or OIDC identity for profile %d", i+1)
		}
		ids[p.ID] = true
		names[strings.ToLower(p.Name)] = true
		identities[identity] = true
		prepared, err := prepareProfile(p, historyOnly)
		if err != nil {
			return nil, fmt.Errorf("profile %d: %w", i+1, err)
		}
		result = append(result, prepared)
	}
	return result, nil
}

func prepareProfile(p ImportedProfile, historyOnly bool) (preparedProfile, error) {
	result := preparedProfile{profile: p}
	if p.Model != defaultModel && p.Model != slimModel {
		return result, fmt.Errorf("invalid skin model %q", p.Model)
	}
	if !historyOnly && p.PNG != nil {
		data, err := skin.Normalize(bytes.NewReader(p.PNG), p.Model)
		if err != nil {
			return result, fmt.Errorf("normalize current skin: %w", err)
		}
		result.profile.PNG = data
		result.hash = digest(string(data))
	}
	seen := map[string]bool{}
	for _, entry := range p.History {
		data, err := skin.Normalize(bytes.NewReader(entry.PNG), entry.Model)
		if err != nil {
			return result, fmt.Errorf("normalize history skin: %w", err)
		}
		hash := digest(string(data))
		key := hash + ":" + entry.Model
		if seen[key] {
			continue
		}
		seen[key] = true
		result.history = append(
			result.history,
			importedTexture{ImportedSkin: ImportedSkin{Model: entry.Model, PNG: data}, hash: hash},
		)
	}
	return result, nil
}

func openImportDatabase(ctx context.Context, dir string, apply, historyOnly bool) (*sql.DB, error) {
	path, err := filepath.Abs(filepath.Join(dir, "atto.db"))
	if err != nil {
		return nil, fmt.Errorf("resolve import database: %w", err)
	}
	if historyOnly || !apply {
		if _, err = os.Stat(path); err != nil {
			return nil, fmt.Errorf("inspect import database: %w", err)
		}
	}
	if apply {
		return openDB(ctx, dir)
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open read-only import database: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func emptyImportResult(profiles []preparedProfile) ImportResult {
	result := ImportResult{Added: len(profiles)}
	for _, p := range profiles {
		for _, entry := range p.history {
			if entry.hash == p.hash && entry.Model == p.profile.Model {
				result.HistoryUnchanged++
			} else {
				result.HistoryAdded++
			}
		}
	}
	return result
}

func importRecord(ctx context.Context, tx *sql.Tx, p preparedProfile, mode importMode, result *ImportResult) error {
	q := queries.New(tx)
	profile := p.profile
	matches, err := q.MatchImportUser(
		ctx,
		queries.MatchImportUserParams{
			ID:      profile.ID,
			Issuer:  profile.Issuer,
			Subject: profile.Subject,
			Column4: !mode.historyOnly,
			Name:    profile.Name,
		},
	)
	if err != nil {
		return fmt.Errorf("match imported identity: %w", err)
	}
	currentHash, currentModel, err := prepareTargetRecord(ctx, tx, p, mode, matches, result)
	if err != nil {
		return err
	}
	for _, entry := range p.history {
		exists := entry.hash == currentHash && entry.Model == currentModel
		if mode.version >= 2 && (len(matches) > 0 || mode.apply) {
			exists, err = q.HasHistorySkin(
				ctx,
				queries.HasHistorySkinParams{UserID: profile.ID, SkinHash: entry.hash, Model: entry.Model},
			)
			if err != nil {
				return fmt.Errorf("find imported history: %w", err)
			}
		}
		if err = importHistoryEntry(ctx, q, profile.ID, entry, exists, mode.apply, result); err != nil {
			return err
		}
	}
	return nil
}

func checkImportMatch(
	ctx context.Context,
	q *queries.Queries,
	p preparedProfile,
	matches []queries.MatchImportUserRow,
	historyOnly bool,
) error {
	match, profile := matches[0], p.profile
	identical := match.ID == profile.ID && match.Issuer == profile.Issuer && match.Subject == profile.Subject
	if !historyOnly {
		identical = identical && match.Name == profile.Name && match.SkinHash == p.hash && match.Model == profile.Model
	}
	if len(matches) != 1 || !identical {
		return fmt.Errorf(
			"target conflict for profile %q: UUID, name or OIDC account is already in use; no profiles were committed",
			profile.Name,
		)
	}
	if !historyOnly && p.hash != "" {
		png, err := q.GetTexture(ctx, p.hash)
		if err != nil {
			return fmt.Errorf("read existing current skin: %w", err)
		}
		if !bytes.Equal(png, profile.PNG) {
			return fmt.Errorf("existing profile %q has inconsistent texture data", profile.Name)
		}
	}
	return nil
}

func insertImportedUser(ctx context.Context, tx *sql.Tx, p preparedProfile) error {
	q, profile := queries.New(tx), p.profile
	if profile.PNG != nil {
		if _, err := q.PutTexture(ctx, queries.PutTextureParams{Hash: p.hash, Png: profile.PNG}); err != nil {
			return fmt.Errorf("import current texture: %w", err)
		}
	}
	if _, err := q.ImportUser(
		ctx,
		queries.ImportUserParams{
			ID:       profile.ID,
			Issuer:   profile.Issuer,
			Subject:  profile.Subject,
			Name:     profile.Name,
			SkinHash: p.hash,
			Model:    profile.Model,
		},
	); err != nil {
		return fmt.Errorf("import user: %w", err)
	}
	if p.hash != "" {
		return rememberSkin(ctx, tx, profile.ID, p.hash, profile.Model)
	}
	return nil
}

func importHistoryEntry(
	ctx context.Context,
	q *queries.Queries,
	uid string,
	entry importedTexture,
	exists, apply bool,
	result *ImportResult,
) error {
	stored, err := q.GetTexture(ctx, entry.hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read history texture: %w", err)
	}
	if err == nil && !bytes.Equal(stored, entry.PNG) {
		return fmt.Errorf("inconsistent history texture for profile %q", uid)
	}
	if exists {
		result.HistoryUnchanged++
		return nil
	}
	result.HistoryAdded++
	if !apply {
		return nil
	}
	if _, err = q.PutTexture(ctx, queries.PutTextureParams{Hash: entry.hash, Png: entry.PNG}); err != nil {
		return fmt.Errorf("import history texture: %w", err)
	}
	if _, err = q.ImportHistorySkin(
		ctx,
		queries.ImportHistorySkinParams{UserID: uid, SkinHash: entry.hash, Model: entry.Model},
	); err != nil {
		return fmt.Errorf("import history ownership: %w", err)
	}
	return nil
}

func prepareTargetRecord(
	ctx context.Context,
	tx *sql.Tx,
	p preparedProfile,
	mode importMode,
	matches []queries.MatchImportUserRow,
	result *ImportResult,
) (string, string, error) {
	if len(matches) > 0 {
		if err := checkImportMatch(ctx, queries.New(tx), p, matches, mode.historyOnly); err != nil {
			return "", "", err
		}
		result.Unchanged++
		return matches[0].SkinHash, matches[0].Model, nil
	}
	if mode.historyOnly {
		return "", "", fmt.Errorf(
			"history-only: profile %q has not been migrated; no history was committed",
			p.profile.Name,
		)
	}
	if mode.apply {
		if err := insertImportedUser(ctx, tx, p); err != nil {
			return "", "", err
		}
	}
	result.Added++
	return p.hash, p.profile.Model, nil
}
