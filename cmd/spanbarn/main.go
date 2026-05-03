package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/config"
)

// Version and BuildTime are injected at build time via -ldflags.
var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// run owns process wiring: it opens storage, starts the worker, and serves the API.
func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg := config.Load()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("spanbarn %s (built %s)\n", Version, BuildTime)
			return nil
		case "worker-once":
			// TODO: replay queued spool records into persistent store
			slog.Info("worker-once: not yet implemented")
			return nil
		case "user":
			// TODO: user create --username=X --password=Y
			slog.Info("user subcommand: not yet implemented")
			return nil
		case "project":
			// TODO: project create --name=X [--slug=Y]
			slog.Info("project subcommand: not yet implemented")
			return nil
		case "apikey":
			// TODO: apikey create --project=default --name=my-app
			slog.Info("apikey subcommand: not yet implemented")
			return nil
		}
	}

	if cfg.SessionSecret == "" {
		slog.Warn("SPANBARN_SESSION_SECRET is not set; sessions will not persist across restarts")
	}

	// TODO: Open SQLite storage
	// store, err := storage.Open(cfg.DBPath)
	// if err != nil { return err }
	// defer store.Close()
	slog.Info("storage", "path", cfg.DBPath)

	// TODO: Initialize spool
	// eventSpool, err := spool.NewWithLimit(cfg.SpoolDir, cfg.MaxSpoolBytes)
	// if err != nil { return err }
	// defer eventSpool.Close()
	slog.Info("spool", "dir", cfg.SpoolDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// TODO: Start background worker
	// go runBackgroundWorker(ctx, eventSpool, cfg.SpoolDir, store)

	// TODO: Start HTTP server with real handler
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, Version)
	})

	server := &http.Server{
		Addr:    cfg.Addr,
		Handler: mux,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
