package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/zhyupe/atto-skin-go/internal/command"
	"github.com/zhyupe/atto-skin-go/internal/logging"

	"github.com/zhyupe/atto-skin-go/internal/server"
)

func run(ctx context.Context, args []string) error {
	cmd := &cobra.Command{Use: "atto-skin-go", SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs}
	config := viper.New()
	defaults := map[string]string{
		"base-url":           "http://localhost:8080",
		"data-dir":           "data",
		"listen-addr":        ":8080",
		"oidc-issuer":        "",
		"oidc-client-id":     "",
		"oidc-client-secret": "",
		"trusted-proxies":    "",
	}
	for name, value := range defaults {
		cmd.Flags().String(name, value, name)
		if err := config.BindPFlag(name, cmd.Flags().Lookup(name)); err != nil {
			return fmt.Errorf("bind flag %s: %w", name, err)
		}
		if err := config.BindEnv(name, strings.ToUpper(strings.ReplaceAll(name, "-", "_"))); err != nil {
			return fmt.Errorf("bind environment %s: %w", name, err)
		}
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg := server.Config{
			BaseURL:      config.GetString("base-url"),
			DataDir:      config.GetString("data-dir"),
			Issuer:       config.GetString("oidc-issuer"),
			ClientID:     config.GetString("oidc-client-id"),
			ClientSecret: config.GetString("oidc-client-secret"),
		}
		for raw := range strings.SplitSeq(config.GetString("trusted-proxies"), ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return fmt.Errorf("parse trusted proxy: %w", err)
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, prefix)
		}
		return serve(cmd.Context(), cfg, config.GetString("listen-addr"))
	}
	cmd.SetArgs(command.NormalizeArgs(args, cmd.Flags()))
	if err := cmd.ExecuteContext(ctx); err != nil {
		return fmt.Errorf("run skin server: %w", err)
	}
	return nil
}

func serve(ctx context.Context, cfg server.Config, listen string) error {
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	app, err := server.New(startup, cfg)
	cancel()
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	defer app.Close()
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); app.Maintenance(workerCtx) }()
	defer func() { stopWorker(); <-workerDone }()
	srv := &http.Server{
		Addr:              listen,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	logging.From(ctx).WithField("listen", srv.Addr).WithField("base_url", cfg.BaseURL).Info("Atto Skin started")
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdown, stopShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopShutdown()
		if err = srv.Shutdown(shutdown); err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	}
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		stop()
		os.Exit(1)
	}

	stop()
}
