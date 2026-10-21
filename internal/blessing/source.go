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
)

type Account struct {
	UID        int64
	Email      string
	Permission int
}
type Player struct {
	PID, UID, SkinID, CapeID int64
	Name                     string
}
type Texture struct {
	ID         int64
	Type, Hash string
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
	if cfg.MultiStatements || cfg.AllowAllFiles || cfg.AllowCleartextPasswords || cfg.AllowFallbackToPlaintext || cfg.TLSConfig == "skip-verify" || cfg.TLSConfig == "preferred" {
		return nil, errors.New("DSN must not enable multiStatements, arbitrary local files, cleartext authentication or unverified/fallback TLS")
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
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to source MySQL: %w", err)
	}
	return db, nil
}

// Read uses only SELECTs in a repeatable-read, read-only transaction. Stop writes
// to the old site while taking its database and filesystem backups.
func Read(ctx context.Context, db *sql.DB, prefix string) (*Snapshot, error) {
	if !prefixPattern.MatchString(prefix) || len(prefix) > 40 {
		return nil, errors.New("table prefix must contain at most 40 ASCII letters, digits or underscores")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	columns := map[string]map[string]bool{}
	rows, err := tx.QueryContext(ctx, "SELECT TABLE_NAME,COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME IN (?,?,?,?,?)", prefix+"users", prefix+"players", prefix+"textures", prefix+"uuid", prefix+"options")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var table, col string
		if err = rows.Scan(&table, &col); err != nil {
			rows.Close()
			return nil, err
		}
		if columns[table] == nil {
			columns[table] = map[string]bool{}
		}
		columns[table][col] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for table, required := range map[string][]string{"users": {"uid", "email", "permission"}, "players": {"pid", "uid", "tid_skin", "tid_cape"}, "textures": {"tid", "type", "hash"}} {
		for _, col := range required {
			if !columns[prefix+table][col] {
				return nil, fmt.Errorf("unsupported Blessing schema: missing %s.%s; upgrade old schemas to Blessing Skin 5/6 first", prefix+table, col)
			}
		}
	}
	name := "name"
	if !columns[prefix+"players"][name] {
		name = "player_name"
		if !columns[prefix+"players"][name] {
			return nil, errors.New("players table has neither name nor player_name")
		}
	}
	table := func(name string) string { return "`" + prefix + name + "`" }
	s := &Snapshot{Accounts: map[int64]Account{}, Textures: map[int64]Texture{}}
	rows, err = tx.QueryContext(ctx, "SELECT uid,email,permission FROM "+table("users")+" ORDER BY uid")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a Account
		if err = rows.Scan(&a.UID, &a.Email, &a.Permission); err != nil {
			rows.Close()
			return nil, err
		}
		s.Accounts[a.UID] = a
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT pid,uid,`"+name+"`,tid_skin,tid_cape FROM "+table("players")+" ORDER BY uid,pid")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p Player
		if err = rows.Scan(&p.PID, &p.UID, &p.Name, &p.SkinID, &p.CapeID); err != nil {
			rows.Close()
			return nil, err
		}
		s.Players = append(s.Players, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT tid,type,hash FROM "+table("textures")+" WHERE tid IN (SELECT tid_skin FROM "+table("players")+")")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t Texture
		if err = rows.Scan(&t.ID, &t.Type, &t.Hash); err != nil {
			rows.Close()
			return nil, err
		}
		s.Textures[t.ID] = t
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if cols := columns[prefix+"uuid"]; len(cols) > 0 {
		if !cols["name"] || !cols["uuid"] {
			return nil, errors.New("unsupported uuid table: expected name and uuid")
		}
		pid := "NULL"
		s.UUIDByPID = cols["pid"]
		if s.UUIDByPID {
			pid = "pid"
		}
		rows, err = tx.QueryContext(ctx, "SELECT "+pid+",name,uuid FROM "+table("uuid"))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var u UUIDRecord
			if err = rows.Scan(&u.PID, &u.Name, &u.UUID); err != nil {
				rows.Close()
				return nil, err
			}
			s.UUIDs = append(s.UUIDs, u)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if cols := columns[prefix+"options"]; cols["option_name"] && cols["option_value"] {
		rows, err = tx.QueryContext(ctx, "SELECT option_value FROM "+table("options")+" WHERE option_name='ygg_uuid_algorithm'")
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			count++
			if err = rows.Scan(&s.Algorithm); err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if count > 1 {
			return nil, errors.New("duplicate ygg_uuid_algorithm options")
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s, nil
}
