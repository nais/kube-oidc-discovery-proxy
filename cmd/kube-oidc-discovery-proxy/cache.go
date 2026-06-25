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

	"golang.org/x/sync/singleflight"
)

const (
	// maxBodyBytes caps a cached response. Discovery documents and JWKS are tiny.
	maxBodyBytes = 1 << 20 // 1 MiB
	// fetchTimeout bounds a single upstream request.
	fetchTimeout = 10 * time.Second
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
	base http.RoundTripper
	ttl  time.Duration
	log  *slog.Logger

	sf      singleflight.Group
	mu      sync.RWMutex
	entries map[string]*cachedResponse
}

func newCachingTransport(base http.RoundTripper, ttl time.Duration, log *slog.Logger) *cachingTransport {
	return &cachingTransport{
		base:    base,
		ttl:     ttl,
		log:     log,
		entries: make(map[string]*cachedResponse),
	}
}

func (t *cachingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.URL.Host + req.URL.Path
	if e := t.fresh(key); e != nil {
		return responseFromCache(req, e), nil
	}

	v, err, _ := t.sf.Do(key, func() (any, error) {
		// Another caller may have refreshed while this one waited on the group.
		if e := t.fresh(key); e != nil {
			return e, nil
		}
		e, err := t.load(req)
		if err != nil {
			if stale := t.cached(key); stale != nil {
				t.log.With("path", req.URL.Path, "upstream", req.URL.Host, "err", err).
					Warn("serving stale cache after upstream error")
				return stale, nil
			}
			return nil, err
		}
		// Only cache good documents; non-200s are passed through uncached.
		if e.status == http.StatusOK {
			t.mu.Lock()
			t.entries[key] = e
			t.mu.Unlock()
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
