// Command notes is the Carseph Notes server.
//
// Usage:
//
//	notes [serve]          run the HTTP server (default)
//	notes invite-new       mint a one-time invite code and print it
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"notes/internal/config"
	"notes/internal/httpapi"
	"notes/internal/sessions"
	"notes/internal/store"
	"notes/internal/webauthn"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(os.Args) > 1 {
		switch cmd := os.Args[1]; cmd {
		case "invite-new":
			inviteNew()
			return
		case "serve", "-serve", "--serve":
			serve()
		case "-h", "--help", "help":
			usage()
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
			usage()
			os.Exit(2)
		}
		return
	}
	serve()
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: notes [command]

commands:
  serve        run the HTTP server (default)
  invite-new   mint a one-time invite code and print it
`)
}

// serve runs the HTTP server until SIGINT/SIGTERM.
func serve() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "carseph.db"))
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	wa, err := webauthn.New(cfg.RPID, cfg.RPName, cfg.Origin, st)
	if err != nil {
		slog.Error("webauthn init", "err", err)
		os.Exit(1)
	}

	sm := &sessions.Manager{TTL: cfg.SessionTTL, Secure: !cfg.Dev}
	srv := httpapi.New(cfg, st, wa, sm)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Background cleanup of expired sessions.
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			n, err := st.DeleteExpiredSessions(ctx)
			if err != nil {
				slog.Warn("session cleanup", "err", err)
			} else if n > 0 {
				slog.Info("session cleanup", "removed", n)
			}
			cancel()
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.ListenAddr, "origin", cfg.Origin, "rp_id", cfg.RPID)
		errCh <- httpServer.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server", "err", err)
			os.Exit(1)
		}
	case sig := <-stop:
		slog.Info("shutting down", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			slog.Warn("shutdown", "err", err)
		}
	}
}

// inviteNew mints an invite code. Usage: notes invite-new
func inviteNew() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "carseph.db"))
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	code, err := st.CreateInviteCode(context.Background())
	if err != nil {
		slog.Error("mint invite", "err", err)
		os.Exit(1)
	}
	// Print only the code on stdout so scripts can capture it.
	fmt.Println(strings.TrimSpace(code))
}
