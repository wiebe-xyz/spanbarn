package main

import (
	"log/slog"
	"os"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/observability"
)

// Version, Commit and BuildTime are injected at build time via -ldflags. Commit
// and BuildTime are facts about the binary. Version starts as the build's
// default and is replaced by SPANBARN_VERSION when the deploy sets it, because
// the image is built once per commit and retagged to the semver at release, so
// the semver is only known to the deploy.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// runtimeVersion returns the deploy-provided version, or the build's default.
func runtimeVersion(buildDefault string) string {
	if v := os.Getenv("SPANBARN_VERSION"); v != "" {
		return v
	}
	return buildDefault
}

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
	// Resolve once, before anything reads Version, so health, /version, traces
	// and release tags cannot disagree.
	Version = runtimeVersion(Version)
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
