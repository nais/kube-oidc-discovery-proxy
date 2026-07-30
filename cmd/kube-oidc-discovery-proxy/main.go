package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"
)

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		slog.Error("parse flags", "err", err)
		os.Exit(2)
	}

	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, cfg, log); err != nil {
		log.With("err", err).Error("fatal")
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg Config, log *slog.Logger) error {
	handler, err := newHandler(ctx, cfg.Routes, log)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.BindAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Shutdown runs only after ctx is cancelled, so its timeout must derive from
	// a fresh context rather than the already-cancelled ctx.
	go func() { // #nosec G118 -- shutdown deliberately uses a fresh context, not the cancelled ctx
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.With("err", err).Error("shutdown")
		}
	}()

	log.With("addr", cfg.BindAddress).Info("kube-oidc-discovery-proxy serving")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
