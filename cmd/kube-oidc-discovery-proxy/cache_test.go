package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newTestGauge() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_upstream_up"}, []string{"upstream", "path"})
}

// roundTrip drives the transport the way ReverseProxy would: it issues a GET to
// the upstream and returns the buffered body and response.
func roundTrip(t *testing.T, tr http.RoundTripper, target, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://"+target+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, string(body)
}

func TestCachingTransportServesFromCacheWithinTTL(t *testing.T) {
	var hits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=300")
		_, _ = io.WriteString(w, `{"issuer":"x"}`)
	}))
	defer srv.Close()

	tr := newCachingTransport(srv.Client().Transport, time.Minute, discardLogger(), newTestGauge())
	addr := srv.Listener.Addr().String()

	for range 3 {
		resp, body := roundTrip(t, tr, addr, "/openid/v1/jwks")
		if body != `{"issuer":"x"}` {
			t.Fatalf("unexpected body %q", body)
		}
		if got := resp.Header.Get("Cache-Control"); got != "max-age=300" {
			t.Fatalf("upstream header not passed through: %q", got)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", got)
	}
}

func TestCachingTransportRefetchesAfterTTL(t *testing.T) {
	var hits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	tr := newCachingTransport(srv.Client().Transport, time.Nanosecond, discardLogger(), newTestGauge())
	addr := srv.Listener.Addr().String()

	roundTrip(t, tr, addr, "/openid/v1/jwks")
	time.Sleep(time.Millisecond)
	roundTrip(t, tr, addr, "/openid/v1/jwks")

	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("expected 2 upstream hits after TTL expiry, got %d", got)
	}
}

func TestCachingTransportSingleflightCollapsesConcurrentRefresh(t *testing.T) {
	var hits int32
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	tr := newCachingTransport(srv.Client().Transport, time.Minute, discardLogger(), newTestGauge())
	addr := srv.Listener.Addr().String()

	var wg sync.WaitGroup
	// First caller triggers the fetch and blocks in the handler.
	wg.Go(func() { roundTrip(t, tr, addr, "/openid/v1/jwks") })
	<-entered // upstream request is now in flight

	// Additional callers arrive while the fetch is in flight; they must join the
	// in-flight singleflight call rather than issue new upstream requests.
	for range 5 {
		wg.Go(func() { roundTrip(t, tr, addr, "/openid/v1/jwks") })
	}
	time.Sleep(20 * time.Millisecond) // let late callers register on the group
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected singleflight to collapse to 1 upstream hit, got %d", got)
	}
}

func TestCachingTransportServesStaleOnUpstreamError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "fresh")
	}))
	addr := srv.Listener.Addr().String()

	// ttl=0 makes every entry immediately stale, forcing a refetch each call.
	tr := newCachingTransport(srv.Client().Transport, 0, discardLogger(), newTestGauge())

	_, body := roundTrip(t, tr, addr, "/openid/v1/jwks") // primes the cache
	if body != "fresh" {
		t.Fatalf("unexpected body %q", body)
	}

	srv.Close() // upstream now unreachable

	_, body = roundTrip(t, tr, addr, "/openid/v1/jwks")
	if body != "fresh" {
		t.Fatalf("expected stale body, got %q", body)
	}
}

func TestCachingTransportDoesNotCacheNon200(t *testing.T) {
	var hits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tr := newCachingTransport(srv.Client().Transport, time.Minute, discardLogger(), newTestGauge())
	addr := srv.Listener.Addr().String()

	resp, _ := roundTrip(t, tr, addr, "/openid/v1/jwks")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 passthrough, got %d", resp.StatusCode)
	}
	roundTrip(t, tr, addr, "/openid/v1/jwks")
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("non-200 must not be cached: expected 2 hits, got %d", got)
	}
}

// TestMonitorForcesSecondUpstreamHitAfterTicker verifies that the monitor
// issues a second upstream request after the ticker fires, even with no client
// traffic and an unexpired cache entry.
func TestMonitorForcesSecondUpstreamHitAfterTicker(t *testing.T) {
	var hits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"issuer":"x"}`)
	}))
	defer srv.Close()

	g := newTestGauge()
	// long TTL so normal requests would not refetch
	tr := newCachingTransport(srv.Client().Transport, time.Minute, discardLogger(), g)
	addr := srv.Listener.Addr().String()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Use a very short interval so the ticker fires quickly.
	interval := 20 * time.Millisecond
	go tr.runMonitor(ctx, addr, []string{"/openid/v1/jwks"}, interval)

	// Wait for at least 2 upstream hits (immediate poll + at least one ticker tick).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&hits) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&hits); got < 2 {
		t.Fatalf("expected ≥2 upstream hits from monitor ticks, got %d", got)
	}

	// Gauge must show 1 (success).
	if v := testutil.ToFloat64(g.WithLabelValues(addr, "/openid/v1/jwks")); v != 1 {
		t.Errorf("expected gauge 1, got %v", v)
	}
}

func TestMonitorKeepsStaleCacheAfterRefreshFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "cached")
	}))
	addr := srv.Listener.Addr().String()

	tr := newCachingTransport(srv.Client().Transport, time.Minute, discardLogger(), newTestGauge())
	_, body := roundTrip(t, tr, addr, "/openid/v1/jwks")
	if body != "cached" {
		t.Fatalf("unexpected body %q", body)
	}

	srv.Close()
	tr.forceRefresh(t.Context(), addr, "/openid/v1/jwks")

	_, body = roundTrip(t, tr, addr, "/openid/v1/jwks")
	if body != "cached" {
		t.Fatalf("expected stale body after monitor failure, got %q", body)
	}
}

// TestMonitorStopsOnContextCancellation verifies that after ctx is cancelled,
// no further upstream hits occur.
func TestMonitorStopsOnContextCancellation(t *testing.T) {
	var hits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = io.WriteString(w, `{"issuer":"x"}`)
	}))
	defer srv.Close()

	tr := newCachingTransport(srv.Client().Transport, time.Minute, discardLogger(), newTestGauge())
	addr := srv.Listener.Addr().String()

	ctx, cancel := context.WithCancel(t.Context())

	// Short interval so we can confirm the monitor is running before cancelling.
	interval := 20 * time.Millisecond
	done := make(chan struct{})
	go func() {
		tr.runMonitor(ctx, addr, []string{"/openid/v1/jwks"}, interval)
		close(done)
	}()

	// Wait for at least one hit to confirm the monitor started.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&hits) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&hits) == 0 {
		t.Fatal("monitor never fired")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor goroutine did not stop after context cancellation")
	}

	// No more hits after cancel.
	hitsBefore := atomic.LoadInt32(&hits)
	time.Sleep(3 * interval)
	if after := atomic.LoadInt32(&hits); after > hitsBefore {
		t.Errorf("monitor continued hitting upstream after cancel: before=%d after=%d", hitsBefore, after)
	}
}
