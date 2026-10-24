package blessing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/zhyupe/atto-skin-go/internal/server"
)

// This test requires a disposable MySQL database and an admin DSN. It creates
// uniquely prefixed fixture tables and a SELECT-only user, then removes them.
func TestMySQLIntegration(t *testing.T) {
	dsn := os.Getenv("ATTO_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set ATTO_TEST_MYSQL_DSN to a disposable MySQL/MariaDB database")
	}
	for _, connect := range []bool{false, true} {
		t.Run(fmt.Sprintf("connect=%v", connect), func(t *testing.T) {
			ctx := context.Background()
			admin, err := OpenMySQL(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { admin.Close() })
			cfg, err := mysql.ParseDSN(dsn)
			if err != nil {
				t.Fatal(err)
			}
			suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
			prefix := "atto_it_" + suffix + "_"
			readerUser := "atto_it_" + suffix
			exec := func(query string, args ...any) {
				t.Helper()
				if _, e := admin.ExecContext(ctx, query, args...); e != nil {
					t.Fatal(query, e)
				}
			}
			tables := []string{"users", "players", "textures", "uuid", "options"}
			t.Cleanup(func() {
				for _, table := range tables {
					if _, e := admin.Exec("DROP TABLE IF EXISTS `" + prefix + table + "`"); e != nil {
						t.Error(e)
					}
				}
				if _, e := admin.Exec("DROP USER IF EXISTS '" + readerUser + "'@'%'"); e != nil {
					t.Error(e)
				}
			})
			nameColumn := "name"
			uuidColumns := "name VARCHAR(50),uuid VARCHAR(36)"
			if connect {
				nameColumn = "player_name"
				uuidColumns = "pid INT NULL," + uuidColumns
			}
			exec(
				"CREATE TABLE `" + prefix + "users`(uid INT PRIMARY KEY,email VARCHAR(100),permission INT,password VARCHAR(255)) ENGINE=InnoDB",
			)
			exec(
				"CREATE TABLE `" + prefix + "players`(pid INT PRIMARY KEY,uid INT,`" + nameColumn + "` VARCHAR(50),tid_skin INT,tid_cape INT) ENGINE=InnoDB",
			)
			exec(
				"CREATE TABLE `" + prefix + "textures`(tid INT PRIMARY KEY,type VARCHAR(10),hash VARCHAR(64),uploader INT NULL,public BOOLEAN) ENGINE=InnoDB",
			)
			exec("CREATE TABLE `" + prefix + "uuid`(" + uuidColumns + ") ENGINE=InnoDB")
			exec("CREATE TABLE `" + prefix + "options`(option_name VARCHAR(100),option_value TEXT) ENGINE=InnoDB")
			exec(
				"INSERT INTO `" + prefix + "users` VALUES(1,'alice@example.invalid',0,'never-migrate-passwords'),(2,'banned@example.invalid',-1,'ignored'),(3,'empty@example.invalid',0,'ignored'),(4,'bob@example.invalid',0,'ignored')",
			)
			exec(
				"INSERT INTO `" + prefix + "players` VALUES(10,1,'Alice',7,8),(11,1,'AltAlice',0,0),(20,2,'Banned',0,0),(40,4,'Bob',0,0)",
			)
			data := pngFixture(t, 64, 64)
			h := sha256.Sum256(data)
			hash := hex.EncodeToString(h[:])
			exec(
				"INSERT INTO `"+prefix+"textures` VALUES(7,'alex',?,4,1),(9,'steve',?,1,0),(10,'alex',?,1,0),(11,'steve','unused-null-owner',NULL,1),(12,'steve','unused-no-player',3,0),(13,'cape','unused-cape',1,0),(14,'steve','unused-banned',2,0),(15,'steve',?,1,1)",
				hash,
				hash,
				hash,
				hash,
			)
			uuid := "12345678-1234-4234-8234-123456789abc"
			if connect {
				exec("INSERT INTO `"+prefix+"uuid` VALUES(10,'Alice',?)", uuid)
			} else {
				exec("INSERT INTO `"+prefix+"uuid` VALUES('Alice',?)", uuid)
			}
			exec("INSERT INTO `" + prefix + "options` VALUES('ygg_uuid_algorithm','v3')")
			exec("CREATE USER '" + readerUser + "'@'%' IDENTIFIED BY 'fixture-password'")
			exec("GRANT SELECT ON `" + strings.ReplaceAll(cfg.DBName, "`", "``") + "`.* TO '" + readerUser + "'@'%'")
			readerCfg := *cfg
			readerCfg.User = readerUser
			readerCfg.Passwd = "fixture-password"
			source, err := OpenMySQL(ctx, readerCfg.FormatDSN())
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if _, err = source.Exec("DELETE FROM `" + prefix + "users`"); err == nil {
				t.Fatal("fixture user must be read-only")
			}
			snapshot, err := Read(ctx, source, prefix)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Accounts) != 4 || len(snapshot.Players) != 4 || len(snapshot.Textures) != 8 ||
				snapshot.Textures[7].Uploader != 4 ||
				snapshot.UUIDByPID != connect {
				t.Fatal("source snapshot mismatch")
			}
			dir := t.TempDir()
			if err = os.WriteFile(filepath.Join(dir, hash), data, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "mapping.csv")
			if err = WriteTemplate(path, snapshot); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			m, err := ReadMapping(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			m[0].Action = "import"
			m[0].Subject = "subject-alice"
			m[1].Action = "skip"
			m[3].Action = "import"
			m[3].Subject = "subject-bob"
			plan, err := BuildPlan(snapshot, m, "https://id.example", dir)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "target")
			result, err := server.ImportProfiles(ctx, target, plan.Profiles, false)
			if err != nil || result.Added != 2 || result.HistoryAdded != 2 || result.HistoryUnchanged != 1 {
				t.Fatal("history dry run", result, err)
			}
			if _, err = os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("dry run wrote a target")
			}
			result, err = server.ImportProfiles(ctx, target, plan.Profiles, true)
			if err != nil || result.Added != 2 || result.HistoryAdded != 2 || result.HistoryUnchanged != 1 {
				t.Fatal("history commit", result, err)
			}
			dest, err := sql.Open("sqlite", filepath.Join(target, "atto.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer dest.Close()
			var id, sub, model, skinHash, secret string
			if err = dest.QueryRow("SELECT id,subject,model,skin_hash,secret_hash FROM users WHERE subject='subject-alice'").
				Scan(&id, &sub, &model, &skinHash, &secret); err != nil {
				t.Fatal(err)
			}
			if id != "12345678123442348234123456789abc" || sub != "subject-alice" || model != "slim" ||
				skinHash != hash ||
				secret != "" {
				t.Fatal("incorrect migrated profile")
			}
			result, err = server.ImportProfiles(ctx, target, plan.Profiles, true)
			if err != nil || result.Unchanged != 2 || result.HistoryAdded != 0 || result.HistoryUnchanged != 3 {
				t.Fatal("repeat import", result, err)
			}
			var aliceHistory, bobHistory int
			if err = dest.QueryRow("SELECT count(*) FROM skin_history WHERE user_id=?", id).
				Scan(&aliceHistory); err != nil {
				t.Fatal(err)
			}
			if err = dest.QueryRow("SELECT count(*) FROM skin_history WHERE user_id=?", offlineUUID("Bob")).
				Scan(&bobHistory); err != nil {
				t.Fatal(err)
			}
			if aliceHistory != 2 || bobHistory != 1 || plan.HistorySkipped != 3 {
				t.Fatal("history ownership", aliceHistory, bobHistory, plan.HistorySkipped)
			}
			// Confirm source rows and even their password fields remain untouched.
			var password string
			if err = source.QueryRow("SELECT password FROM `" + prefix + "users` WHERE uid=1").
				Scan(&password); err != nil ||
				password != "never-migrate-passwords" {
				t.Fatal("source changed", err)
			}
		})
	}
}
