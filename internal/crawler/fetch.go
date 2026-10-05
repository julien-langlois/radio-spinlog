package crawler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// httpClient is shared by all scrapers; the timeout keeps a hung connection from freezing a worker
var httpClient = &http.Client{Timeout: 15 * time.Second}

const maxBodySize = 10 << 20 // 10 MiB, far above any "now playing" payload

// page is the last known state of one URL, shared by every radio that reads it
type page struct {
	mu           sync.Mutex
	body         []byte
	err          error
	fetchedAt    time.Time
	notBefore    time.Time // set from Retry-After: no request before this instant
	etag         string
	lastModified string
}

var (
	pagesMu sync.Mutex
	pages   = map[string]*page{}
)

// fetch returns the body of url, hitting the network at most once per maxAge whatever the
// number of radios configured on that URL: concurrent callers wait for the one in-flight
// request and share its result (success or failure). Refreshes are conditional (ETag /
// Last-Modified), so an unchanged source answers 304 with no payload, and Retry-After is obeyed.
// The returned slice is shared: callers must not modify it.
func fetch(ctx context.Context, url, userAgent, accept string, maxAge time.Duration) ([]byte, error) {
	pagesMu.Lock()
	p := pages[url]
	if p == nil {
		p = &page{}
		pages[url] = p
	}
	pagesMu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	fresh := !p.fetchedAt.IsZero() && now.Sub(p.fetchedAt) < maxAge
	if !fresh && !now.Before(p.notBefore) {
		p.err = p.refresh(ctx, url, userAgent, accept)
		p.fetchedAt = time.Now()
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.body, nil
}

// refresh performs the HTTP request; p.body is kept on failure so a later 304 can still be served
func (p *page) refresh(ctx context.Context, url, userAgent, accept string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if p.body != nil {
		if p.etag != "" {
			req.Header.Set("If-None-Match", p.etag)
		}
		if p.lastModified != "" {
			req.Header.Set("If-Modified-Since", p.lastModified)
		}
	}

	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	slog.Debug("HTTP request", "url", url, "status", res.StatusCode)

	switch res.StatusCode {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
	default:
		if wait := retryAfter(res.Header.Get("Retry-After")); wait > 0 {
			p.notBefore = time.Now().Add(wait)
			slog.Warn("Source asked us to slow down, pausing requests", "url", url, "status", res.StatusCode, "wait", wait)
		}
		return fmt.Errorf("unexpected HTTP status %s", res.Status)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxBodySize))
	if err != nil {
		return err
	}
	p.body, p.etag, p.lastModified = body, res.Header.Get("ETag"), res.Header.Get("Last-Modified")
	return nil
}

// retryAfter parses a Retry-After header (delay in seconds or HTTP date), 0 if absent or invalid
func retryAfter(v string) time.Duration {
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}
