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
	db, err := openDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id, secret, hash string
	if err = db.QueryRow("SELECT id,secret_hash,skin_hash FROM users WHERE subject='subject-a'").Scan(&id, &secret, &hash); err != nil {
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
	u, err := app.ensureUser(p.Issuer, p.Subject)
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
			db, err := openDB(dir)
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
