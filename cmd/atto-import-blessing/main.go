// atto-import-blessing reads MySQL and local PNGs, then imports explicitly mapped OIDC profiles.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zhyupe/atto-skin-go/internal/blessing"
	"github.com/zhyupe/atto-skin-go/internal/server"
)

func run(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("atto-import-blessing", flag.ContinueOnError)
	flags.SetOutput(out)
	dsnEnv := flags.String("mysql-dsn-env", "BLESSING_MYSQL_DSN", "environment variable containing the read-only source MySQL DSN")
	prefix := flags.String("table-prefix", "", "Blessing table prefix, e.g. bs_")
	export := flags.String("export-mapping", "", "create a private CSV template (refuses overwrite); no target database is opened")
	mapping := flags.String("mapping", "", "reviewed CSV template; every player must be marked import or skip")
	issuer := flags.String("oidc-issuer", os.Getenv("OIDC_ISSUER"), "target OIDC issuer, exactly matching the server configuration")
	textures := flags.String("textures-dir", "", "copy of Blessing storage/textures")
	dataDir := flags.String("data-dir", "", "Atto data directory (required except when exporting the mapping)")
	apply := flags.Bool("apply", false, "commit the import; default is a read-only dry run")
	historyOnly := flags.Bool("history-only", false, "append uploader library skins to already migrated accounts without changing their current profiles")
	timeout := flags.Duration("timeout", 10*time.Minute, "maximum duration for reading and importing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if (*export == "") == (*mapping == "") {
		return fmt.Errorf("specify exactly one of -export-mapping or -mapping")
	}
	if *export != "" && (*apply || *dataDir != "" || *historyOnly) {
		return fmt.Errorf("-export-mapping cannot be combined with -apply, -data-dir or -history-only")
	}
	if *mapping != "" && (*dataDir == "" || *issuer == "") {
		return fmt.Errorf("-data-dir and -oidc-issuer (or OIDC_ISSUER) are required")
	}
	if *timeout <= 0 {
		return fmt.Errorf("-timeout must be positive")
	}
	dsn := os.Getenv(*dsnEnv)
	if dsn == "" {
		return fmt.Errorf("set %s to a read-only MySQL DSN; do not pass credentials as arguments", *dsnEnv)
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	db, err := blessing.OpenMySQL(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	snapshot, err := blessing.Read(ctx, db, *prefix)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Source: %d accounts, %d players. No source data was changed.\n", len(snapshot.Accounts), len(snapshot.Players))
	if *export != "" {
		if err = blessing.WriteTemplate(*export, snapshot); err != nil {
			return err
		}
		fmt.Fprintf(out, "Created %s. Choose one player per account, set import/skip and fill authoritative OIDC subjects. Missing/review UUIDs must be verified manually.\n", *export)
		return nil
	}
	f, err := os.Open(*mapping)
	if err != nil {
		return err
	}
	mappings, err := blessing.ReadMapping(f)
	f.Close()
	if err != nil {
		return err
	}
	buildPlan, importProfiles := blessing.BuildPlan, server.ImportProfiles
	if *historyOnly {
		buildPlan, importProfiles = blessing.BuildHistoryPlan, server.ImportProfileHistory
	}
	plan, err := buildPlan(snapshot, mappings, *issuer, *textures)
	if err != nil {
		return err
	}
	result, err := importProfiles(ctx, *dataDir, plan.Profiles, *apply)
	if err != nil {
		return err
	}
	mode := "DRY RUN (target unchanged)"
	if *apply {
		mode = "COMMITTED"
	}
	fmt.Fprintf(out, "%s: %d new, %d existing profiles preserved, %d assigned skins; %d players explicitly skipped, %d accounts without players, %d capes omitted.\n", mode, result.Added, result.Unchanged, plan.Skins, plan.Skipped, plan.AccountsWithoutPlayers, plan.CapesOmitted)
	fmt.Fprintf(out, "Library history: %d source skins selected by uploader, %d new entries, %d already present; %d library skins omitted because their uploaders are not mapped.\n", plan.HistorySkins, result.HistoryAdded, result.HistoryUnchanged, plan.HistorySkipped)
	fmt.Fprintln(out, "Passwords, launcher tokens, closets, permissions and signing keys are not migrated. Imported users log in with OIDC and generate new launcher keys.")
	return nil
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		if err == flag.ErrHelp {
			return
		}
		fmt.Fprintln(os.Stderr, "Import failed:", err)
		os.Exit(1)
	}
}
