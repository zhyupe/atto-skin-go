package blessing

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture() *Snapshot {
	return &Snapshot{
		Accounts: map[int64]Account{
			1: {UID: 1, Email: "alice@example.invalid"},
			2: {UID: 2, Permission: -1},
			3: {UID: 3},
		},
		Players: []Player{
			{PID: 10, UID: 1, Name: "Alice"},
			{PID: 11, UID: 1, Name: "AltAlice"},
			{PID: 20, UID: 2, Name: "Banned"},
		},
		Textures:  map[int64]Texture{},
		Algorithm: "v3",
	}
}
func decisions() []Mapping {
	return []Mapping{
		{
			Action:     "import",
			UID:        1,
			PID:        10,
			SourceName: "Alice",
			UUID:       offlineUUID("Alice"),
			Subject:    "oidc-sub-alice",
			Name:       "Alice",
		},
		{Action: "skip", UID: 1, PID: 11, SourceName: "AltAlice"},
		{Action: "skip", UID: 2, PID: 20, SourceName: "Banned"},
	}
}
func pngFixture(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func attachSkin(t *testing.T, s *Snapshot, dir string, data []byte, typ string) {
	t.Helper()
	h := sha256.Sum256(data)
	hash := hex.EncodeToString(h[:])
	if err := os.WriteFile(filepath.Join(dir, hash), data, 0600); err != nil {
		t.Fatal(err)
	}
	s.Players[0].SkinID = 7
	s.Textures[7] = Texture{ID: 7, Hash: hash, Type: typ}
}

func TestUUIDPreservation(t *testing.T) {
	if got := offlineUUID("Notch"); got != "b50ad385829d3141a2167e7d7539ba7f" {
		t.Fatal("incorrect offline UUID", got)
	}
	p := Player{PID: 10, Name: "Alice"}
	s := fixture()
	want := "12345678123442348234123456789abc"
	s.UUIDs = []UUIDRecord{{Name: "Alice", UUID: "12345678-1234-4234-8234-123456789abc"}}
	if got, source := s.ResolveUUID(p); got != want || source != "uuid-table" {
		t.Fatal(got, source)
	}
	s.UUIDs = append(s.UUIDs, s.UUIDs[0])
	if got, source := s.ResolveUUID(p); got != "" || source != "review-duplicate-uuid" {
		t.Fatal("ambiguous UUID selected")
	}
	s.UUIDByPID = true
	s.UUIDs = []UUIDRecord{{PID: sql.NullInt64{Int64: 10, Valid: true}, Name: "Alice", UUID: want}}
	if got, _ := s.ResolveUUID(p); got != want {
		t.Fatal("PID UUID not retained")
	}
	s.UUIDs[0].PID.Int64 = 99
	if got, source := s.ResolveUUID(p); got != "" || source != "review-orphaned-uuid" {
		t.Fatal(got, source)
	}
	s.UUIDs = nil
	s.Algorithm = "v4"
	if got, source := s.ResolveUUID(p); got != "" || source != "manual-required" {
		t.Fatal("random identity fabricated")
	}
	if _, err := normalizeUUID("1234-5678123442348234123456789abc"); err == nil {
		t.Fatal("malformed UUID accepted")
	}
}
func TestTemplateAndDecisions(t *testing.T) {
	s := fixture()
	path := filepath.Join(t.TempDir(), "mapping.csv")
	if err := WriteTemplate(path, s); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("mapping permissions")
	}
	if err := WriteTemplate(path, s); err == nil {
		t.Fatal("overwrote reviewed mapping")
	}
	raw, _ := os.ReadFile(path)
	m, err := ReadMapping(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m[0].Action != "pending" || m[0].Subject != "" || m[2].Action != "skip" {
		t.Fatal("unsafe template defaults")
	}
	if _, err = BuildPlan(s, m, "https://id.example", ""); err == nil {
		t.Fatal("unreviewed mapping accepted")
	}
	m = decisions()
	plan, err := BuildPlan(s, m, "https://id.example", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Profiles) != 1 || plan.Skipped != 2 || plan.AccountsWithoutPlayers != 1 ||
		plan.Profiles[0].ID != offlineUUID("Alice") {
		t.Fatal("incorrect plan", plan)
	}
	m[0].Name = "NewName"
	plan, err = BuildPlan(s, m, "https://id.example", "")
	if err != nil || plan.Profiles[0].ID != offlineUUID("Alice") {
		t.Fatal("explicit rename must keep old UUID")
	}
	// CSV quoting, including commas and quotes in an issuer's subject, must round-trip.
	records, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	records[1][0] = "import"
	records[1][8] = `sub,"a"`
	records[2][0] = "skip"
	var b bytes.Buffer
	writer := csv.NewWriter(&b)
	writer.WriteAll(records)
	parsed, err := ReadMapping(strings.NewReader("\ufeff" + b.String()))
	if err != nil || parsed[0].Subject != `sub,"a"` {
		t.Fatal("CSV subject corrupted")
	}
}
func TestMappingRejectsAmbiguity(t *testing.T) {
	cases := map[string]func(*Snapshot, []Mapping) []Mapping{
		"two-players": func(_ *Snapshot, m []Mapping) []Mapping {
			m[1].Action = "import"
			m[1].Subject = "other"
			m[1].UUID = offlineUUID("AltAlice")
			m[1].Name = "AltAlice"
			return m
		},
		"missing-decision":    func(_ *Snapshot, m []Mapping) []Mapping { return m[:2] },
		"duplicate-pid":       func(_ *Snapshot, m []Mapping) []Mapping { return append(m, m[0]) },
		"changed-owner":       func(_ *Snapshot, m []Mapping) []Mapping { m[0].UID = 5; return m },
		"changed-source-name": func(_ *Snapshot, m []Mapping) []Mapping { m[0].SourceName = "Changed"; return m },
		"missing-sub":         func(_ *Snapshot, m []Mapping) []Mapping { m[0].Subject = ""; return m },
		"bad-name":            func(_ *Snapshot, m []Mapping) []Mapping { m[0].Name = "invalid name"; return m },
		"changed-uuid":        func(_ *Snapshot, m []Mapping) []Mapping { m[0].UUID = offlineUUID("Other"); return m },
		"banned": func(s *Snapshot, m []Mapping) []Mapping {
			a := s.Accounts[1]
			a.Permission = -1
			s.Accounts[1] = a
			return m
		},
		"missing-texture": func(s *Snapshot, m []Mapping) []Mapping { s.Players[0].SkinID = 999; return m },
		"legacy-texture":  func(s *Snapshot, m []Mapping) []Mapping { s.Players[0].SkinID = -1; return m },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := fixture()
			m := change(s, decisions())
			if _, err := BuildPlan(s, m, "https://id.example", ""); err == nil {
				t.Fatal("unsafe plan accepted")
			}
		})
	}
	s := fixture()
	s.Algorithm = "v4"
	m := decisions()
	m[0].UUID = "12345678123442348234123456789abc"
	if _, err := BuildPlan(s, m, "https://id.example", ""); err != nil {
		t.Fatal("explicit verified UUID should work", err)
	}
}
func TestTexturesAreVerifiedAndSanitized(t *testing.T) {
	s := fixture()
	dir := t.TempDir()
	original := pngFixture(t, 64, 64)
	raw := append(append([]byte{}, original...), []byte("trailing-data")...)
	attachSkin(t, s, dir, raw, "alex")
	s.Players[0].CapeID = 99
	plan, err := BuildPlan(s, decisions(), "https://id.example", dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Skins != 1 || plan.CapesOmitted != 1 || plan.Profiles[0].Model != "slim" ||
		!bytes.Equal(plan.Profiles[0].PNG, original) {
		t.Fatal("texture was not canonicalized")
	}
	texture := s.Textures[7]
	if err = os.Rename(filepath.Join(dir, texture.Hash), filepath.Join(dir, texture.Hash+".png")); err != nil {
		t.Fatal(err)
	}
	if _, err = BuildPlan(s, decisions(), "https://id.example", dir); err != nil {
		t.Fatal("PNG extension fallback", err)
	}
	os.WriteFile(filepath.Join(dir, texture.Hash+".png"), []byte("corrupt"), 0600)
	if _, err = BuildPlan(s, decisions(), "https://id.example", dir); err == nil {
		t.Fatal("hash mismatch ignored")
	}
	attachSkin(t, s, dir, pngFixture(t, 128, 128), "steve")
	if _, err = BuildPlan(s, decisions(), "https://id.example", dir); err == nil {
		t.Fatal("HD skin silently changed")
	}
	texture = s.Textures[7]
	texture.Hash = "../../secret"
	s.Textures[7] = texture
	if _, err = BuildPlan(s, decisions(), "https://id.example", dir); err == nil {
		t.Fatal("path traversal accepted")
	}
	// A correctly named symlink outside the selected backup root must not be followed.
	outside := t.TempDir()
	attachSkin(t, s, outside, original, "steve")
	texture = s.Textures[7]
	if err = os.Symlink(filepath.Join(outside, texture.Hash), filepath.Join(dir, texture.Hash)); err != nil {
		t.Fatal(err)
	}
	if _, err = BuildPlan(s, decisions(), "https://id.example", dir); err == nil {
		t.Fatal("escaped texture root")
	}
}

func TestHistoryPlanUsesUploadersAndSkipsAssignments(t *testing.T) {
	s := fixture()
	dir := t.TempDir()
	attachSkin(t, s, dir, pngFixture(t, 64, 64), "alex")
	uploaded := s.Textures[7]
	uploaded.Uploader = 1
	s.Textures[7] = uploaded
	// No assigned skin is needed to discover an uploader's library.
	s.Players[0].SkinID = 999
	s.Textures[8] = Texture{ID: 8, Uploader: 2, Type: "steve", Hash: "missing-banned-skin"}
	s.Textures[9] = Texture{ID: 9, Uploader: 3, Type: "steve", Hash: "missing-no-player-skin"}
	s.Textures[10] = Texture{ID: 10, Type: "alex", Hash: "orphan"}
	s.Textures[11] = Texture{ID: 11, Uploader: 1, Type: "cape", Hash: "missing-cape"}
	plan, err := BuildHistoryPlan(s, decisions(), "https://id.example", dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Skins != 0 || plan.HistorySkins != 1 || plan.HistorySkipped != 3 || len(plan.Profiles[0].History) != 1 ||
		plan.Profiles[0].History[0].Model != "slim" ||
		plan.Profiles[0].PNG != nil {
		t.Fatal("incorrect uploader history", plan)
	}
	if _, err = BuildPlan(s, decisions(), "https://id.example", dir); err == nil {
		t.Fatal("full import ignored missing assigned skin")
	}
	// Owned library files are validated even when they were never assigned.
	if err = os.Remove(filepath.Join(dir, uploaded.Hash)); err != nil {
		t.Fatal(err)
	}
	if _, err = BuildHistoryPlan(s, decisions(), "https://id.example", dir); err == nil {
		t.Fatal("missing owned history silently omitted")
	}
}
