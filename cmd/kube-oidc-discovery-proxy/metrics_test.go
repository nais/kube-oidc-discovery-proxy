package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsScrapeContainsUpstreamUp(t *testing.T) {
	routes := []route{{Host: "proxy.test", Upstream: "upstream.example.com"}}

	ctx := t.Context()

	h, err := newHandler(ctx, routes, time.Minute, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/internal/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("metrics: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "kube_oidc_discovery_proxy_upstream_up") {
		t.Errorf("metrics body missing kube_oidc_discovery_proxy_upstream_up; got:\n%s", body)
	}
}

func TestUpstreamUpGauge1On200(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"issuer":"x"}`)
	}))
	defer srv.Close()

	_, upstreamUp := newRegistry()
	tr := newCachingTransport(srv.Client().Transport, time.Minute, slog.New(slog.DiscardHandler), upstreamUp)
	addr := srv.Listener.Addr().String()

	roundTrip(t, tr, addr, "/openid/v1/jwks")

	if v := testutil.ToFloat64(upstreamUp.WithLabelValues(addr, "/openid/v1/jwks")); v != 1 {
		t.Errorf("expected gauge 1 after 200, got %v", v)
	}
}

func TestUpstreamUpGauge0OnTransportError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	addr := srv.Listener.Addr().String()

	_, upstreamUp := newRegistry()
	// ttl=0 so every request re-fetches.
	tr := newCachingTransport(srv.Client().Transport, 0, slog.New(slog.DiscardHandler), upstreamUp)
	roundTrip(t, tr, addr, "/openid/v1/jwks") // prime
	srv.Close()                               // now unreachable

	// Next call triggers a fetch that fails; stale is served but metric → 0.
	roundTrip(t, tr, addr, "/openid/v1/jwks")
	if v := testutil.ToFloat64(upstreamUp.WithLabelValues(addr, "/openid/v1/jwks")); v != 0 {
		t.Errorf("expected gauge 0 after transport error, got %v", v)
	}
}

func TestUpstreamUpGauge0OnNon200(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, upstreamUp := newRegistry()
	tr := newCachingTransport(srv.Client().Transport, time.Minute, slog.New(slog.DiscardHandler), upstreamUp)
	addr := srv.Listener.Addr().String()

	roundTrip(t, tr, addr, "/openid/v1/jwks")
	if v := testutil.ToFloat64(upstreamUp.WithLabelValues(addr, "/openid/v1/jwks")); v != 0 {
		t.Errorf("expected gauge 0 after non-200, got %v", v)
	}
}

// TestMonitorUpdatesGaugeWithoutClientTraffic verifies that the background
// monitor sets upstream_up=1 via the immediate poll, without any proxy request.
func TestMonitorUpdatesGaugeWithoutClientTraffic(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"issuer":"x"}`)
	}))
	defer srv.Close()

	_, upstreamUp := newRegistry()
	tr := newCachingTransport(srv.Client().Transport, 0, slog.New(slog.DiscardHandler), upstreamUp)
	addr := srv.Listener.Addr().String()

	// Pre-initialise at 0 (mirrors newHandler behaviour).
	for path := range allowedPaths {
		upstreamUp.WithLabelValues(addr, path).Set(0)
	}

	ctx := t.Context()

	paths := []string{"/openid/v1/jwks"}
	tr.startMonitor(ctx, addr, paths, monitorInterval)

	// Wait for the immediate poll to complete (up to 2 s).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v := testutil.ToFloat64(upstreamUp.WithLabelValues(addr, "/openid/v1/jwks")); v == 1 {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("expected gauge 1 from monitor poll without client traffic, got %v",
		testutil.ToFloat64(upstreamUp.WithLabelValues(addr, "/openid/v1/jwks")))
}
