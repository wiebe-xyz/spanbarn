package main

import (
	"log/slog"
	"os"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/observability"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	// A panic on the main goroutine would exit before the 2s BugBarn flush
	// tick, so report it synchronously and let the process still die.
	defer observability.RecoverAndReport("main", true)
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger, shutdownObservability := observability.Setup(Version)
	slog.SetDefault(logger)
	defer shutdownObservability()

	cfg := config.Load()

	if handled, err := dispatch(cfg, os.Args[1:], os.Stdout); handled {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	// Trust proxy forwarding headers for client-IP determination only when
	// configured (default: on outside dev), so rate limiting keys on the real
	// client behind Caddy/Nginx instead of the shared proxy IP.
	api.SetTrustProxy(cfg.TrustProxy)

	// Honour X-Forwarded-Proto (Secure cookie flag + HSTS) only from trusted
	// proxy peers; with no allowlist configured outside dev, assume HTTPS —
	// every named deployment terminates TLS upstream.
	api.SetSecurePolicy(cfg.TrustedProxies, !cfg.IsDevEnvironment())

	switch cfg.Mode {
	case "ingest":
		return runIngestMode(cfg, logger)
	case "reader":
		return runReaderMode(cfg, logger)
	case "writer":
		return runWriterMode(cfg, logger)
	default:
		return runStandalone(cfg, logger)
	}
}
