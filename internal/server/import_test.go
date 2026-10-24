package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func migrationProfile(id, name, subject string) ImportedProfile {
	return ImportedProfile{ID: id, Issuer: "https://id.example", Subject: subject, Name: name, Model: "default"}
}
func TestImportDryRunCommitAndIdempotence(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "target")
	p := migrationProfile("12345678123442348234123456789abc", "Alice", "subject-a")
	p.PNG = pngData(t, 64, 64)
	result, err := ImportProfiles(ctx, dir, []ImportedProfile{p}, false)
	if err != nil || result.Added != 1 {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("dry run created target")
	}
	result, err = ImportProfiles(ctx, dir, []ImportedProfile{p}, true)
	if err != nil || result.Added != 1 {
		t.Fatal(result, err)
	}
	db, err := openDB(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id, secret, hash string
	if err = db.QueryRow("SELECT id,secret_hash,skin_hash FROM users WHERE subject='subject-a'").
		Scan(&id, &secret, &hash); err != nil {
		t.Fatal(err)
	}
	if id != p.ID || secret != "" || hash == "" {
		t.Fatal("identity or skin missing")
	}
	db.Exec("UPDATE users SET secret_hash='existing-secret' WHERE id=?", id)
	for _, apply := range []bool{false, true} {
		result, err = ImportProfiles(ctx, dir, []ImportedProfile{p}, apply)
		if err != nil || result.Unchanged != 1 || result.Added != 0 {
			t.Fatal(result, err)
		}
	}
	db.QueryRow("SELECT secret_hash FROM users").Scan(&secret)
	if secret != "existing-secret" {
		t.Fatal("idempotent import reset credentials")
	}
	// The normal OIDC login path reuses the migrated identity, rather than creating a user.
	app := &Server{db: db}
	u, err := app.ensureUser(t.Context(), p.Issuer, p.Subject)
	if err != nil || u.ID != p.ID || u.Name != p.Name {
		t.Fatal("OIDC did not reuse imported profile", err)
	}
}
func TestImportConflictsRollbackWholeBatch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	existing := migrationProfile("12345678123442348234123456789abc", "Alice", "subject-a")
	if _, err := ImportProfiles(ctx, dir, []ImportedProfile{existing}, true); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"name", "uuid", "subject", "changed-skin"} {
		t.Run(kind, func(t *testing.T) {
			first := migrationProfile("22345678123442348234123456789abc", "First", "subject-first")
			first.PNG = pngData(t, 64, 64)
			conflict := migrationProfile("32345678123442348234123456789abc", "Other", "subject-other")
			switch kind {
			case "name":
				conflict.Name = "alice"
			case "uuid":
				conflict.ID = existing.ID
			case "subject":
				conflict.Subject = existing.Subject
			case "changed-skin":
				conflict = existing
				conflict.PNG = pngData(t, 64, 64)
			}
			for _, apply := range []bool{false, true} {
				if _, err := ImportProfiles(ctx, dir, []ImportedProfile{first, conflict}, apply); err == nil {
					t.Fatal("conflict accepted")
				}
			}
			db, err := openDB(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var n int
			db.QueryRow("SELECT count(*) FROM users").Scan(&n)
			if n != 1 {
				t.Fatal("partial user import", n)
			}
			db.QueryRow("SELECT count(*) FROM textures").Scan(&n)
			if n != 0 {
				t.Fatal("partial texture import", n)
			}
		})
	}
}
func TestImportRejectsDuplicateIdentitiesBeforeWriting(t *testing.T) {
	p := migrationProfile("12345678123442348234123456789abc", "Alice", "subject-a")
	for _, apply := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "target")
		if _, err := ImportProfiles(context.Background(), dir, []ImportedProfile{p, p}, apply); err == nil {
			t.Fatal("duplicate accepted")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("validation created target")
		}
	}
}

func TestImportHistoryBackfillPreservesProfileAndRecency(t *testing.T) {
	ctx, dir := context.Background(), t.TempDir()
	p := migrationProfile("12345678123442348234123456789abc", "Alice", "subject-a")
	p.PNG = pngData(t, 64, 64)
	if _, err := ImportProfiles(ctx, dir, []ImportedProfile{p}, true); err != nil {
		t.Fatal(err)
	}
	db, err := openDB(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(
		"UPDATE users SET name='Renamed',skin_hash='',model='slim',secret_hash='keep-secret' WHERE id=?",
		p.ID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE skin_history SET last_used=123456 WHERE user_id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	// A duplicate record deduplicates; the same PNG with a different model is distinct.
	p.History = []ImportedSkin{{Model: "default", PNG: p.PNG}, {Model: "slim", PNG: p.PNG}, {Model: "slim", PNG: p.PNG}}
	for _, apply := range []bool{false, true, true} {
		result, err := ImportProfileHistory(ctx, dir, []ImportedProfile{p}, apply)
		if err != nil || result.Added != 0 || result.Unchanged != 1 {
			t.Fatal(result, err)
		}
		var name, hash, model, secret string
		if err = db.QueryRow("SELECT name,skin_hash,model,secret_hash FROM users WHERE id=?", p.ID).
			Scan(&name, &hash, &model, &secret); err != nil {
			t.Fatal(err)
		}
		if name != "Renamed" || hash != "" || model != "slim" || secret != "keep-secret" {
			t.Fatal("backfill changed profile")
		}
		var count, recency int
		if err = db.QueryRow("SELECT count(*) FROM skin_history WHERE user_id=?", p.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if !apply && (count != 1 || result.HistoryAdded != 1 || result.HistoryUnchanged != 1) {
			t.Fatal("dry run wrote history or wrong counts", count, result)
		}
		if apply && count != 2 {
			t.Fatal("duplicate history", count)
		}
		if err = db.QueryRow("SELECT last_used FROM skin_history WHERE user_id=? AND model='default'", p.ID).
			Scan(&recency); err != nil ||
			recency != 123456 {
			t.Fatal("changed existing recency", recency, err)
		}
		if apply {
			if err = db.QueryRow("SELECT last_used FROM skin_history WHERE user_id=? AND model='slim'", p.ID).
				Scan(&recency); err != nil ||
				recency != 0 {
				t.Fatal("backfill marked skin as worn", recency, err)
			}
		}
	}
	result, err := ImportProfileHistory(ctx, dir, []ImportedProfile{p}, false)
	if err != nil || result.HistoryAdded != 0 || result.HistoryUnchanged != 2 {
		t.Fatal("repeat stats", result, err)
	}
}

func TestImportHistoryRejectsIdentityMismatchAndRollsBack(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"missing", "uuid", "subject", "issuer"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			p := migrationProfile("12345678123442348234123456789abc", "Alice", "subject-a")
			other := migrationProfile("22345678123442348234123456789abc", "Bob", "subject-b")
			if _, err := ImportProfiles(ctx, dir, []ImportedProfile{p, other}, true); err != nil {
				t.Fatal(err)
			}
			p.History = []ImportedSkin{{Model: "default", PNG: pngData(t, 64, 64)}}
			switch kind {
			case "missing":
				other.ID = "32345678123442348234123456789abc"
				other.Subject = "missing"
			case "uuid":
				other.ID = "32345678123442348234123456789abc"
			case "subject":
				other.Subject = "changed"
			case "issuer":
				other.Issuer = "https://changed.example"
			}
			for _, apply := range []bool{false, true} {
				if _, err := ImportProfileHistory(ctx, dir, []ImportedProfile{p, other}, apply); err == nil {
					t.Fatal("history identity conflict accepted")
				}
			}
			db, err := openDB(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, table := range []string{"textures", "skin_history"} {
				var n int
				if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
					t.Fatal("partial history committed", table, n, err)
				}
			}
		})
	}
	p := migrationProfile("12345678123442348234123456789abc", "Alice", "subject-a")
	for _, apply := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "missing")
		if _, err := ImportProfileHistory(ctx, dir, []ImportedProfile{p}, apply); err == nil {
			t.Fatal("created target for history-only")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("history-only created target")
		}
	}
}

func TestImportLibraryAtomicValidationAndIdempotence(t *testing.T) {
	ctx := context.Background()
	p := migrationProfile("12345678123442348234123456789abc", "Alice", "subject-a")
	p.PNG = pngData(t, 64, 64)
	p.History = []ImportedSkin{{Model: "default", PNG: p.PNG}, {Model: "slim", PNG: p.PNG}, {Model: "slim", PNG: p.PNG}}
	dir := filepath.Join(t.TempDir(), "target")
	for _, apply := range []bool{false, true} {
		result, err := ImportProfiles(ctx, dir, []ImportedProfile{p}, apply)
		if err != nil || result.HistoryAdded != 1 || result.HistoryUnchanged != 1 {
			t.Fatal("new import counts", result, err)
		}
	}
	result, err := ImportProfiles(ctx, dir, []ImportedProfile{p}, true)
	if err != nil || result.HistoryAdded != 0 || result.HistoryUnchanged != 2 {
		t.Fatal("repeat full import", result, err)
	}
	for _, entry := range []ImportedSkin{{Model: "invalid", PNG: p.PNG}, {Model: "default", PNG: []byte("bad")}, {Model: "slim", PNG: pngData(t, 64, 32)}} {
		p.History = []ImportedSkin{entry}
		dir := filepath.Join(t.TempDir(), "target")
		if _, err := ImportProfiles(ctx, dir, []ImportedProfile{p}, true); err == nil {
			t.Fatal("invalid historical PNG accepted")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("validation created target")
		}
	}
}
