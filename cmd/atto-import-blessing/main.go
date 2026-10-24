// atto-import-blessing imports mapped OIDC profiles from a read-only Blessing snapshot.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/zhyupe/atto-skin-go/internal/blessing"
	"github.com/zhyupe/atto-skin-go/internal/command"
	"github.com/zhyupe/atto-skin-go/internal/server"
)

type importOptions struct {
	dsnEnv, prefix, export, mapping, issuer, textures, dataDir string
	apply, historyOnly                                         bool
	timeout                                                    time.Duration
}

func run(ctx context.Context, args []string, out io.Writer) error {
	cmd := &cobra.Command{Use: "atto-import-blessing", SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs}
	cmd.SetOut(out)
	cmd.SetErr(out)
	config := viper.New()
	if err := config.BindEnv("issuer", "OIDC_ISSUER"); err != nil {
		return fmt.Errorf("bind issuer configuration: %w", err)
	}
	opts := importOptions{}
	flags := cmd.Flags()
	flags.StringVar(
		&opts.dsnEnv,
		"mysql-dsn-env",
		"BLESSING_MYSQL_DSN",
		"environment variable containing the read-only source MySQL DSN",
	)
	flags.StringVar(&opts.prefix, "table-prefix", "", "Blessing table prefix, e.g. bs_")
	flags.StringVar(&opts.export, "export-mapping", "", "create a private CSV template; refuses overwrite")
	flags.StringVar(&opts.mapping, "mapping", "", "reviewed CSV template; mark every player import or skip")
	flags.StringVar(&opts.issuer, "oidc-issuer", config.GetString("issuer"), "target OIDC issuer")
	flags.StringVar(&opts.textures, "textures-dir", "", "copy of Blessing storage/textures")
	flags.StringVar(&opts.dataDir, "data-dir", "", "Atto data directory")
	flags.BoolVar(&opts.apply, "apply", false, "commit the import; default is a read-only dry run")
	flags.BoolVar(&opts.historyOnly, "history-only", false, "append uploaded library skins to existing accounts")
	flags.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "maximum duration for reading and importing")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return executeImport(cmd.Context(), opts, out) }
	cmd.SetArgs(command.NormalizeArgs(args, cmd.Flags()))
	if err := cmd.ExecuteContext(ctx); err != nil {
		return fmt.Errorf("run Blessing import: %w", err)
	}
	return nil
}

func (opts importOptions) validate() error {
	if (opts.export == "") == (opts.mapping == "") {
		return fmt.Errorf("specify exactly one of -export-mapping or -mapping")
	}
	if opts.export != "" && (opts.apply || opts.dataDir != "" || opts.historyOnly) {
		return fmt.Errorf("-export-mapping cannot be combined with -apply, -data-dir or -history-only")
	}
	if opts.mapping != "" && (opts.dataDir == "" || opts.issuer == "") {
		return fmt.Errorf("-data-dir and -oidc-issuer (or OIDC_ISSUER) are required")
	}
	if opts.timeout <= 0 {
		return fmt.Errorf("-timeout must be positive")
	}
	return nil
}

func executeImport(ctx context.Context, opts importOptions, out io.Writer) error {
	if err := opts.validate(); err != nil {
		return err
	}
	dsn := os.Getenv(opts.dsnEnv)
	if dsn == "" {
		return fmt.Errorf("set %s to a read-only MySQL DSN; do not pass credentials as arguments", opts.dsnEnv)
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	db, err := blessing.OpenMySQL(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open source database: %w", err)
	}
	defer func() { _ = db.Close() }()
	snapshot, err := blessing.Read(ctx, db, opts.prefix)
	if err != nil {
		return fmt.Errorf("read source snapshot: %w", err)
	}
	fmt.Fprintf(
		out,
		"Source: %d accounts, %d players. No source data was changed.\n",
		len(snapshot.Accounts),
		len(snapshot.Players),
	)
	if opts.export != "" {
		if err = blessing.WriteTemplate(opts.export, snapshot); err != nil {
			return fmt.Errorf("export identity mapping: %w", err)
		}
		fmt.Fprintf(
			out,
			"Created %s. Choose one player per account, set import/skip and fill authoritative OIDC subjects. Missing/review UUIDs must be verified manually.\n",
			opts.export,
		)
		return nil
	}
	return importSnapshot(ctx, opts, snapshot, out)
}

func importSnapshot(ctx context.Context, opts importOptions, snapshot *blessing.Snapshot, out io.Writer) error {
	f, err := os.Open(opts.mapping)
	if err != nil {
		return fmt.Errorf("open identity mapping: %w", err)
	}
	defer func() { _ = f.Close() }()
	mappings, err := blessing.ReadMapping(f)
	if err != nil {
		return fmt.Errorf("read identity mapping: %w", err)
	}
	buildPlan, importProfiles := blessing.BuildPlan, server.ImportProfiles
	if opts.historyOnly {
		buildPlan, importProfiles = blessing.BuildHistoryPlan, server.ImportProfileHistory
	}
	plan, err := buildPlan(snapshot, mappings, opts.issuer, opts.textures)
	if err != nil {
		return fmt.Errorf("plan source import: %w", err)
	}
	result, err := importProfiles(ctx, opts.dataDir, plan.Profiles, opts.apply)
	if err != nil {
		return fmt.Errorf("import source snapshot: %w", err)
	}
	mode := "DRY RUN (target unchanged)"
	if opts.apply {
		mode = "COMMITTED"
	}
	fmt.Fprintf(
		out,
		"%s: %d new, %d existing profiles preserved, %d assigned skins; %d players explicitly skipped, %d accounts without players, %d capes omitted.\n",
		mode,
		result.Added,
		result.Unchanged,
		plan.Skins,
		plan.Skipped,
		plan.AccountsWithoutPlayers,
		plan.CapesOmitted,
	)
	fmt.Fprintf(
		out,
		"Library history: %d source skins selected by uploader, %d new entries, %d already present; %d library skins omitted because their uploaders are not mapped.\n",
		plan.HistorySkins,
		result.HistoryAdded,
		result.HistoryUnchanged,
		plan.HistorySkipped,
	)
	fmt.Fprintln(
		out,
		"Passwords, launcher tokens, closets, permissions and signing keys are not migrated. Imported users log in with OIDC and generate new launcher keys.",
	)
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Import failed:", err)
		cancel()
		os.Exit(1)
	}
	cancel()
}
