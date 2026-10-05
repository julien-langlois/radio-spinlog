package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchSharesOneRequestPerURL(t *testing.T) {
	var hits, conditional atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte("payload"))
	}))
	defer srv.Close()
	ctx := context.Background()

	// Radios sharing a URL, polling at the same time: one request
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if body, err := fetch(ctx, srv.URL, "test", "", time.Minute); err != nil || string(body) != "payload" {
				t.Errorf("fetch = %q, %v", body, err)
			}
		})
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("got %d requests, want 1", hits.Load())
	}

	// Once stale, the refresh is conditional and a 304 still yields the body
	body, err := fetch(ctx, srv.URL, "test", "", 0)
	if err != nil || string(body) != "payload" || conditional.Load() != 1 {
		t.Fatalf("after 304: body=%q err=%v conditional=%d", body, err, conditional.Load())
	}
}

func TestFetchObeysRetryAfter(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	for range 3 {
		if _, err := fetch(context.Background(), srv.URL, "test", "", 0); err == nil {
			t.Fatal("want an error on 429")
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("got %d requests, want 1 (Retry-After must block the others)", hits.Load())
	}
}
