package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"
)

const (
	// maxBodyBytes caps a cached response. Discovery documents and JWKS are tiny.
	maxBodyBytes = 1 << 20 // 1 MiB
	// fetchTimeout bounds a single upstream request.
	fetchTimeout = 10 * time.Second
	// monitorInterval is the cadence at which the background monitor forces an
	// upstream refresh, independent of cacheTTL.
	monitorInterval = 30 * time.Second
)

// cachedResponse is an immutable, fully buffered snapshot of an upstream
// response. The body is stored as bytes so each caller can be handed an
// independent reader.
type cachedResponse struct {
	status  int
	header  http.Header
	body    []byte
	fetched time.Time
}

// cachingTransport is an http.RoundTripper for use as a ReverseProxy.Transport.
// Discovery endpoints rarely change, so successful (200) responses are cached
// for ttl and shared across requests. singleflight collapses concurrent
// refreshes of the same path into one upstream request, preventing a stampede.
// On upstream failure a stale entry is served if one exists.
type cachingTransport struct {
	base       http.RoundTripper
	ttl        time.Duration
	log        *slog.Logger
	upstreamUp *prometheus.GaugeVec

	sf      singleflight.Group
	mu      sync.RWMutex
	entries map[string]*cachedResponse
}

func newCachingTransport(base http.RoundTripper, ttl time.Duration, log *slog.Logger, upstreamUp *prometheus.GaugeVec) *cachingTransport {
	return &cachingTransport{
		base:       base,
		ttl:        ttl,
		log:        log,
		upstreamUp: upstreamUp,
		entries:    make(map[string]*cachedResponse),
	}
}

func (t *cachingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.roundTrip(req, false)
}

func (t *cachingTransport) roundTrip(req *http.Request, force bool) (*http.Response, error) {
	key := req.URL.Host + req.URL.Path
	if e := t.freshUnlessForced(key, force); e != nil {
		return responseFromCache(req, e), nil
	}

	v, err, _ := t.sf.Do(key, func() (any, error) {
		// Another caller may have refreshed while this one waited on the group.
		if e := t.freshUnlessForced(key, force); e != nil {
			return e, nil
		}
		e, err := t.load(req)
		if err != nil {
			t.upstreamUp.WithLabelValues(req.URL.Host, req.URL.Path).Set(0)
			if stale := t.cached(key); stale != nil {
				t.log.With("path", req.URL.Path, "upstream", req.URL.Host, "err", err).
					Warn("serving stale cache after upstream error")
				return stale, nil
			}
			return nil, err
		}
		if e.status == http.StatusOK {
			t.upstreamUp.WithLabelValues(req.URL.Host, req.URL.Path).Set(1)
			t.mu.Lock()
			t.entries[key] = e
			t.mu.Unlock()
		} else {
			t.upstreamUp.WithLabelValues(req.URL.Host, req.URL.Path).Set(0)
		}
		return e, nil
	})
	if err != nil {
		return nil, err
	}
	return responseFromCache(req, v.(*cachedResponse)), nil
}

// load performs the upstream request and buffers the full response. It uses a
// detached context with its own timeout so a single cancelled client cannot
// abort a fetch shared via singleflight.
func (t *cachingTransport) load(req *http.Request) (*cachedResponse, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), fetchTimeout)
	defer cancel()

	resp, err := t.base.RoundTrip(req.Clone(ctx))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}

	header := resp.Header.Clone()
	// The body is re-served from a fresh reader, so framing headers must not be
	// carried over.
	header.Del("Content-Length")
	header.Del("Transfer-Encoding")

	return &cachedResponse{
		status:  resp.StatusCode,
		header:  header,
		body:    body,
		fetched: time.Now(),
	}, nil
}

func (t *cachingTransport) cached(key string) *cachedResponse {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.entries[key]
}

func (t *cachingTransport) fresh(key string) *cachedResponse {
	if e := t.cached(key); e != nil && time.Since(e.fetched) < t.ttl {
		return e
	}
	return nil
}

func (t *cachingTransport) freshUnlessForced(key string, force bool) *cachedResponse {
	if force {
		return nil
	}
	return t.fresh(key)
}

// responseFromCache builds an independent *http.Response from an immutable
// cache entry, giving each caller its own body reader.
func responseFromCache(req *http.Request, e *cachedResponse) *http.Response {
	return &http.Response{
		StatusCode:    e.status,
		Status:        fmt.Sprintf("%d %s", e.status, http.StatusText(e.status)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        e.header.Clone(),
		Body:          io.NopCloser(bytes.NewReader(e.body)),
		ContentLength: int64(len(e.body)),
		Request:       req,
	}
}

// forceRefresh bypasses the TTL cache and fetches directly from upstream,
// updating the cache entry and gauge. Used by the monitor.
func (t *cachingTransport) forceRefresh(ctx context.Context, upstream, path string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+upstream+path, nil)
	if err != nil {
		return
	}

	resp, err := t.roundTrip(req, true)
	if err != nil {
		t.log.With("upstream", upstream, "path", path, "err", err).Debug("monitor refresh failed")
		return
	}
	_ = resp.Body.Close()
}

// startMonitor launches a background goroutine that forces an upstream refresh
// every interval, keeping the upstream_up gauge current without client traffic.
// The goroutine terminates when ctx is cancelled.
func (t *cachingTransport) startMonitor(ctx context.Context, upstream string, paths []string, interval time.Duration) {
	go t.runMonitor(ctx, upstream, paths, interval)
}

func (t *cachingTransport) runMonitor(ctx context.Context, upstream string, paths []string, interval time.Duration) {
	poll := func() {
		for _, path := range paths {
			if ctx.Err() != nil {
				return
			}
			t.forceRefresh(ctx, upstream, path)
		}
	}

	poll() // immediate first poll
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}
