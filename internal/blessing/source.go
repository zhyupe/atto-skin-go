// Package blessing reads a Blessing Skin snapshot without changing the source database.
package blessing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

type Account struct {
	UID        int64
	Email      string
	Permission int
}
type Player struct {
	PID    int64  `db:"pid"`
	UID    int64  `db:"uid"`
	SkinID int64  `db:"tid_skin"`
	CapeID int64  `db:"tid_cape"`
	Name   string `db:"name"`
}
type Texture struct {
	ID       int64  `db:"tid"`
	Uploader int64  `db:"uploader"`
	Type     string `db:"type"`
	Hash     string `db:"hash"`
}
type UUIDRecord struct {
	PID        sql.NullInt64
	Name, UUID string
}
type Snapshot struct {
	Accounts  map[int64]Account
	Players   []Player
	Textures  map[int64]Texture
	UUIDs     []UUIDRecord
	UUIDByPID bool
	Algorithm string
}

var prefixPattern = regexp.MustCompile(`^[A-Za-z0-9_]*$`)

func OpenMySQL(ctx context.Context, dsn string) (*sql.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid MySQL DSN in environment variable")
	}
	if cfg.DBName == "" {
		return nil, errors.New("MySQL DSN must specify the source database")
	}
	if cfg.MultiStatements || cfg.AllowAllFiles || cfg.AllowCleartextPasswords || cfg.AllowFallbackToPlaintext ||
		cfg.TLSConfig == "skip-verify" ||
		cfg.TLSConfig == "preferred" {
		return nil, errors.New(
			"DSN must not enable multiStatements, arbitrary local files, cleartext authentication or unverified/fallback TLS",
		)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 30 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	cfg.ParseTime = false
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("open my sql: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to source MySQL: %w", err)
	}
	return db, nil
}

// Read takes a consistent source snapshot without changing the source database.
func Read(ctx context.Context, db *sql.DB, prefix string) (*Snapshot, error) {
	if !prefixPattern.MatchString(prefix) || len(prefix) > 40 {
		return nil, errors.New("table prefix must contain at most 40 ASCII letters, digits or underscores")
	}
	tx, err := sqlx.NewDb(db, "mysql").BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin source snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	columns, err := readColumns(ctx, tx, prefix)
	if err != nil {
		return nil, err
	}
	name, err := playerNameColumn(columns, prefix)
	if err != nil {
		return nil, err
	}
	accounts, err := selectSource[Account](ctx, tx, sourceQuery(accountsSQL, prefix, name, ""))
	if err != nil {
		return nil, err
	}
	players, err := selectSource[Player](ctx, tx, sourceQuery(playersSQL, prefix, name, ""))
	if err != nil {
		return nil, err
	}
	textures, err := selectSource[Texture](ctx, tx, sourceQuery(texturesSQL, prefix, name, ""))
	if err != nil {
		return nil, err
	}
	snapshot := &Snapshot{Accounts: map[int64]Account{}, Players: players, Textures: map[int64]Texture{}}
	for _, a := range accounts {
		snapshot.Accounts[a.UID] = a
	}
	for _, texture := range textures {
		snapshot.Textures[texture.ID] = texture
	}
	if err = readUUIDs(ctx, tx, snapshot, columns[prefix+"uuid"], prefix); err != nil {
		return nil, err
	}
	if err = readAlgorithm(ctx, tx, snapshot, columns[prefix+"options"], prefix); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("finish source snapshot: %w", err)
	}
	return snapshot, nil
}

func selectSource[T any](ctx context.Context, tx *sqlx.Tx, query string, args ...any) ([]T, error) {
	var rows []T
	if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("read source rows: %w", err)
	}
	return rows, nil
}

func readColumns(ctx context.Context, tx *sqlx.Tx, prefix string) (map[string]map[string]bool, error) {
	type column struct {
		Table string `db:"TABLE_NAME"`
		Name  string `db:"COLUMN_NAME"`
	}
	rows, err := selectSource[column](
		ctx,
		tx,
		columnsSQL,
		prefix+"users",
		prefix+"players",
		prefix+"textures",
		prefix+"uuid",
		prefix+"options",
	)
	if err != nil {
		return nil, err
	}
	columns := map[string]map[string]bool{}
	for _, row := range rows {
		if columns[row.Table] == nil {
			columns[row.Table] = map[string]bool{}
		}
		columns[row.Table][row.Name] = true
	}
	for table, required := range map[string][]string{"users": {userIDColumn, "email", "permission"}, "players": {playerIDColumn, userIDColumn, "tid_skin", "tid_cape"}, "textures": {"tid", "type", "hash", "uploader"}} {
		for _, col := range required {
			if !columns[prefix+table][col] {
				return nil, fmt.Errorf(
					"unsupported Blessing schema: missing %s.%s; upgrade old schemas to Blessing Skin 5/6 first",
					prefix+table,
					col,
				)
			}
		}
	}
	return columns, nil
}

func playerNameColumn(columns map[string]map[string]bool, prefix string) (string, error) {
	for _, name := range []string{"name", "player_name"} {
		if columns[prefix+"players"][name] {
			return name, nil
		}
	}
	return "", errors.New("players table has neither name nor player_name")
}

func readUUIDs(ctx context.Context, tx *sqlx.Tx, s *Snapshot, columns map[string]bool, prefix string) error {
	if len(columns) == 0 {
		return nil
	}
	if !columns["name"] || !columns["uuid"] {
		return errors.New("unsupported uuid table: expected name and uuid")
	}
	pid := "NULL"
	s.UUIDByPID = columns[playerIDColumn]
	if s.UUIDByPID {
		pid = playerIDColumn
	}
	rows, err := selectSource[UUIDRecord](ctx, tx, sourceQuery(uuidsSQL, prefix, "", pid))
	if err != nil {
		return err
	}
	s.UUIDs = rows
	return nil
}

func readAlgorithm(ctx context.Context, tx *sqlx.Tx, s *Snapshot, columns map[string]bool, prefix string) error {
	if !columns["option_name"] || !columns["option_value"] {
		return nil
	}
	rows, err := selectSource[string](ctx, tx, sourceQuery(algorithmSQL, prefix, "", ""))
	if err != nil {
		return err
	}
	if len(rows) > 1 {
		return errors.New("duplicate ygg_uuid_algorithm options")
	}
	if len(rows) == 1 {
		s.Algorithm = rows[0]
	}
	return nil
}
