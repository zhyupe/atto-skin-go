package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"github.com/jmoiron/sqlx"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // Register the pure-Go SQLite driver.
)

//go:embed migrations/*.sql
var migrations embed.FS

//go:embed schema_version.sql
var SchemaVersion string

const Version = 3

func Open(ctx context.Context, dir string) (*sql.DB, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(dir, "atto.db"))
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open database file: %w", err)
	}
	if err = f.Close(); err != nil {
		return nil, fmt.Errorf("close database file: %w", err)
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	for _, pragma := range []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)"} {
		q.Add("_pragma", pragma)
	}
	u.RawQuery = q.Encode()
	db, err := sqlx.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	success := false
	defer func() {
		if !success {
			_ = db.Close()
		}
	}()
	var version int
	if err = db.GetContext(ctx, &version, SchemaVersion); err != nil {
		return nil, fmt.Errorf("read database version: %w", err)
	}
	if version > Version {
		return nil, fmt.Errorf("database schema %d is newer than supported version %d", version, Version)
	}
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open database migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db.DB, files)
	if err != nil {
		return nil, fmt.Errorf("initialize database migrations: %w", err)
	}
	if _, err = provider.Up(ctx); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	success = true
	return db.DB, nil
}
