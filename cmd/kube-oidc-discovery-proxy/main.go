package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"
)

var cfg = DefaultConfig()

func init() {
	flag.StringVar(&cfg.BindAddress, "bind-address", cfg.BindAddress, "address to listen on")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "which log level to output")
	flag.DurationVar(&cfg.CacheTTL, "cache-ttl", cfg.CacheTTL, "how long to cache upstream discovery responses")
	flag.Var(targets{routes: &cfg.Routes}, "target", "host=upstream route, repeatable")
}

func main() {
	flag.Parse()
	log := newLogger(cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.With("err", err).Error("fatal")
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	handler, err := newHandler(cfg.Routes, cfg.CacheTTL, log)
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
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
