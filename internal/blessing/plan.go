package blessing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/zhyupe/atto-skin-go/internal/validation"

	"github.com/zhyupe/atto-skin-go/internal/server"
	"github.com/zhyupe/atto-skin-go/internal/skin"
)

type Plan struct {
	Profiles                                                                []server.ImportedProfile
	Accounts, Players, Skipped, AccountsWithoutPlayers, Skins, CapesOmitted int
	HistorySkins, HistorySkipped                                            int
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// BuildPlan requires an explicit import/skip decision for every player. The
// caller must supply authoritative OIDC subjects; email addresses are not identity keys.
func BuildPlan(s *Snapshot, mappings []Mapping, issuer, texturesDir string) (Plan, error) {
	return buildPlan(s, mappings, issuer, texturesDir, false)
}

// BuildHistoryPlan selects only library skins uploaded by the mapped accounts.
func BuildHistoryPlan(s *Snapshot, mappings []Mapping, issuer, texturesDir string) (Plan, error) {
	return buildPlan(s, mappings, issuer, texturesDir, true)
}

func buildPlan(s *Snapshot, mappings []Mapping, issuer, texturesDir string, historyOnly bool) (Plan, error) {
	plan := Plan{Accounts: len(s.Accounts), Players: len(s.Players)}
	cfg := server.Config{BaseURL: "http://localhost", Issuer: issuer, ClientID: "migration", DataDir: "unused"}
	if err := cfg.Validate(); err != nil {
		return plan, fmt.Errorf("build plan: %w", err)
	}
	players, indexErr := indexPlayers(s, &plan)
	if indexErr != nil {
		return plan, indexErr
	}
	seenPID := map[int64]bool{}
	seen := selectionSet{
		uid:     map[int64]bool{},
		uuid:    map[string]bool{},
		subject: map[string]bool{},
		name:    map[string]bool{},
	}
	loader := textureLoader{dir: texturesDir, cache: map[string][]byte{}}
	defer loader.close()
	profileByUID := map[int64]int{}
	for _, m := range mappings {
		p, ok := players[m.PID]
		if !ok || p.UID != m.UID || p.Name != m.SourceName {
			return plan, fmt.Errorf(
				"mapping pid %d no longer matches source UID/name; regenerate or review the mapping",
				m.PID,
			)
		}
		if seenPID[p.PID] {
			return plan, fmt.Errorf("duplicate mapping pid %d", p.PID)
		}
		seenPID[p.PID] = true
		if m.Action == "skip" {
			plan.Skipped++
			continue
		}
		if m.Action != "import" {
			return plan, fmt.Errorf("pid %d: choose action import or skip (currently %q)", p.PID, m.Action)
		}
		uuid, err := seen.selectProfile(s, p, m)
		if err != nil {
			return plan, err
		}
		profile := server.ImportedProfile{ID: uuid, Issuer: issuer, Subject: m.Subject, Name: m.Name, Model: "default"}
		if err = assignCurrentSkin(s, p, &profile, &plan, &loader, historyOnly); err != nil {
			return plan, err
		}
		profileByUID[p.UID] = len(plan.Profiles)
		plan.Profiles = append(plan.Profiles, profile)
	}
	if len(seenPID) != len(players) {
		return plan, errors.New(
			"mapping must include every source player with import or skip; regenerate after source changes",
		)
	}
	if len(plan.Profiles) == 0 {
		return plan, errors.New("no players selected for import")
	}
	if err := appendLibrary(s, &plan, profileByUID, &loader); err != nil {
		return plan, err
	}

	return plan, nil
}
func readTexture(root *os.Root, hash, model string) ([]byte, error) {
	f, err := root.Open(hash)
	// The standard Blessing layout is extensionless; allow an exported .png copy as well.
	if errors.Is(err, os.ErrNotExist) {
		f, err = root.Open(hash + ".png")
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filepath.Base(hash), err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("read texture: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > skin.MaxBytes {
		return nil, skin.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, skin.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read texture: %w", err)
	}
	if len(raw) > skin.MaxBytes {
		return nil, skin.ErrInvalid
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, errors.New("PNG content does not match its Blessing SHA-256 filename")
	}
	data, err := skin.Normalize(bytes.NewReader(raw), model)
	if err != nil {
		return nil, fmt.Errorf("normalize source texture: %w", err)
	}
	return data, nil
}

type textureLoader struct {
	dir   string
	root  *os.Root
	cache map[string][]byte
}

func (loader *textureLoader) close() {
	if loader.root != nil {
		_ = loader.root.Close()
	}
}
func (loader *textureLoader) load(t Texture) (server.ImportedSkin, error) {
	entry := server.ImportedSkin{Model: "default"}
	switch t.Type {
	case "steve":
	case "alex":
		entry.Model = "slim"
	default:
		return entry, fmt.Errorf("tid %d: unsupported texture type %q", t.ID, t.Type)
	}
	if !hashPattern.MatchString(t.Hash) {
		return entry, fmt.Errorf("tid %d: invalid texture SHA-256", t.ID)
	}
	key := t.Hash + ":" + entry.Model
	if data, ok := loader.cache[key]; ok {
		entry.PNG = data
		return entry, nil
	}
	if loader.dir == "" {
		return entry, errors.New("-textures-dir is required to import skins; a MySQL dump contains no PNG files")
	}
	var err error
	if loader.root == nil {
		loader.root, err = os.OpenRoot(loader.dir)
		if err != nil {
			return entry, fmt.Errorf("open texture directory: %w", err)
		}
	}
	data, err := readTexture(loader.root, t.Hash, entry.Model)
	if err != nil {
		return entry, fmt.Errorf("tid %d: %w", t.ID, err)
	}
	loader.cache[key] = data
	entry.PNG = data
	return entry, nil
}

type selectionSet struct {
	uid                 map[int64]bool
	uuid, subject, name map[string]bool
}

func (seen *selectionSet) selectProfile(s *Snapshot, p Player, m Mapping) (string, error) {
	a, ok := s.Accounts[p.UID]
	if !ok {
		return "", fmt.Errorf("pid %d has no source account", p.PID)
	}
	if a.Permission < 0 {
		return "", fmt.Errorf(
			"uid %d is banned; skip it and enforce its access restriction at the OIDC provider",
			p.UID,
		)
	}
	if seen.uid[p.UID] {
		return "", fmt.Errorf("uid %d has more than one selected player; choose exactly one", p.UID)
	}
	seen.uid[p.UID] = true
	if m.Subject == "" || strings.TrimSpace(m.Subject) != m.Subject || strings.ContainsRune(m.Subject, 0) {
		return "", fmt.Errorf("pid %d: fill the exact OIDC subject (sub), without surrounding whitespace", p.PID)
	}
	if !validation.PlayerName(m.Name) {
		return "", fmt.Errorf("pid %d: target name must match [A-Za-z0-9_]{3,16}", p.PID)
	}
	uuid, err := normalizeUUID(m.UUID)
	if err != nil {
		return "", fmt.Errorf("pid %d: %w", p.PID, err)
	}
	known, source := s.ResolveUUID(p)
	if known != "" && known != uuid {
		return "", fmt.Errorf(
			"pid %d: profile_uuid differs from %s; refusing to change the existing player identity",
			p.PID,
			source,
		)
	}
	if seen.uuid[uuid] || seen.subject[m.Subject] || seen.name[strings.ToLower(m.Name)] {
		return "", fmt.Errorf("pid %d: duplicate selected UUID, OIDC subject or target name", p.PID)
	}
	seen.uuid[uuid] = true
	seen.subject[m.Subject] = true
	seen.name[strings.ToLower(m.Name)] = true
	return uuid, nil
}

func appendLibrary(s *Snapshot, plan *Plan, profileByUID map[int64]int, loader *textureLoader) error {
	// Stable traversal makes failures reproducible. Ownership comes only from uploader,
	// never from player assignments, public visibility or closet favorites.
	ids := make([]int64, 0, len(s.Textures))
	for id := range s.Textures {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		texture := s.Textures[id]
		if texture.Type == "cape" {
			continue
		}
		index, selected := profileByUID[texture.Uploader]
		if !selected || texture.Uploader <= 0 {
			plan.HistorySkipped++
			continue
		}
		entry, err := loader.load(texture)
		if err != nil {
			return fmt.Errorf("uploader uid %d: %w", texture.Uploader, err)
		}
		plan.Profiles[index].History = append(plan.Profiles[index].History, entry)
		plan.HistorySkins++
	}

	return nil
}

func assignCurrentSkin(
	s *Snapshot,
	p Player,
	profile *server.ImportedProfile,
	plan *Plan,
	loader *textureLoader,
	historyOnly bool,
) error {
	if p.CapeID > 0 {
		plan.CapesOmitted++
	}
	if !historyOnly && p.SkinID > 0 {
		t, ok := s.Textures[p.SkinID]
		if !ok {
			return fmt.Errorf("pid %d: skin tid %d is missing from textures", p.PID, p.SkinID)
		}
		entry, err := loader.load(t)
		if err != nil {
			return fmt.Errorf("pid %d: %w", p.PID, err)
		}
		profile.Model, profile.PNG = entry.Model, entry.PNG
		plan.Skins++
	} else if !historyOnly && p.SkinID < 0 {
		// -1 is the historical lazy migration sentinel, not a reliably empty skin.
		return fmt.Errorf(
			"pid %d has legacy tid_skin=%d; run Blessing's schema/data upgrades before importing",
			p.PID,
			p.SkinID,
		)
	}
	return nil
}

func indexPlayers(s *Snapshot, plan *Plan) (map[int64]Player, error) {
	players := map[int64]Player{}
	owners := map[int64]bool{}
	for _, p := range s.Players {
		if _, ok := players[p.PID]; ok {
			return nil, fmt.Errorf("duplicate source pid %d", p.PID)
		}
		players[p.PID] = p
		owners[p.UID] = true
	}
	for uid := range s.Accounts {
		if !owners[uid] {
			plan.AccountsWithoutPlayers++
		}
	}

	return players, nil
}
