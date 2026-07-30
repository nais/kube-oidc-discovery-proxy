package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// allowedPaths are the only request paths the proxy forwards. They expose the
// OIDC discovery document and JWKS of an upstream Kubernetes apiserver so a
// consumer can validate projected service account tokens minted by that cluster.
var allowedPaths = map[string]bool{
	"/.well-known/openid-configuration": true,
	"/openid/v1/jwks":                   true,
}

func newHandler(ctx context.Context, routes []route, cacheTTL time.Duration, log *slog.Logger) (http.Handler, error) {
	// Validate for duplicate hosts before doing any work.
	seen := make(map[string]struct{}, len(routes))
	for _, r := range routes {
		if _, dup := seen[r.Host]; dup {
			return nil, fmt.Errorf("duplicate route host %q", r.Host)
		}
		seen[r.Host] = struct{}{}
	}

	reg, upstreamUp := newRegistry()

	// Collect allowed paths as a slice for the monitor.
	paths := make([]string, 0, len(allowedPaths))
	for p := range allowedPaths {
		paths = append(paths, p)
	}

	proxies := make(map[string]*httputil.ReverseProxy, len(routes))
	for _, r := range routes {
		target := &url.URL{Scheme: "https", Host: r.Upstream}
		upstream := r.Upstream
		// Pre-initialize gauge labels so they appear in /internal/metrics before the first poll.
		for _, path := range paths {
			upstreamUp.WithLabelValues(upstream, path).Set(0)
		}
		ct := newCachingTransport(http.DefaultTransport, cacheTTL, log.With("upstream", upstream), upstreamUp)
		ct.startMonitor(ctx, upstream, paths, monitorInterval)
		rp := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.Host = upstream
			},
			Transport: ct,
			ErrorLog:  slog.NewLogLogger(log.Handler(), slog.LevelError),
		}
		proxies[r.Host] = rp
		log.With("host", r.Host, "upstream", r.Upstream).Info("registered route")
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /internal/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintln(w, "ok")
	})

	mux.Handle("GET /internal/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !allowedPaths[r.URL.Path] {
			http.NotFound(w, r)
			return
		}
		rp, ok := proxies[r.Host]
		if !ok {
			http.Error(w, "unknown host", http.StatusNotFound)
			return
		}
		// The upstream is not derived from the request: rp is selected from a
		// fixed, configured allowlist of hosts and forwards to a preconfigured
		// upstream, so the request cannot redirect the proxy elsewhere.
		rp.ServeHTTP(w, r) // #nosec G704 -- upstream is a fixed configured allowlist, not request-derived
	})
	return mux, nil
}

func newLogger(level string) *slog.Logger {
	lvl := new(slog.LevelVar)
	switch level {
	case "debug":
		lvl.Set(slog.LevelDebug)
	case "warn", "warning":
		lvl.Set(slog.LevelWarn)
	case "error":
		lvl.Set(slog.LevelError)
	default:
		lvl.Set(slog.LevelInfo)
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
