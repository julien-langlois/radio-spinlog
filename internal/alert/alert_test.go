package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSend(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]string
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &p); err != nil || r.Method != http.MethodPost {
			t.Errorf("bad request: %s %q %v", r.Method, body, err)
		}
		if p["text"] != p["content"] || !strings.HasPrefix(p["text"], "radio-spinlog: ") {
			t.Errorf("text/content = %q / %q", p["text"], p["content"])
		}
		mu.Lock()
		got = append(got, p["event"]+"|"+p["radio"])
		mu.Unlock()
	}))
	defer srv.Close()

	if err := Configure("not a url"); err == nil {
		t.Error("an invalid webhook URL must be rejected")
	}
	Send(TrackLost, "", "dropped: alerts are disabled") // no webhook: nothing happens

	if err := Configure(srv.URL + "/hook/SECRET-TOKEN"); err != nil {
		t.Fatal(err)
	}
	Send(SourceStale, "fr-a", "quiet")
	Send(SourceStale, "fr-a", "quiet again") // same event, same radio: muted by the cooldown
	Send(SourceStale, "fr-b", "quiet")
	Send(SourceRecovered, "fr-a", "back")
	Wait()

	sort.Strings(got)
	if want := "source_recovered|fr-a source_stale|fr-a source_stale|fr-b"; strings.Join(got, " ") != want {
		t.Errorf("got %v, want %s", got, want)
	}

	// A delivery failure is logged without the URL: it carries the webhook's token
	var logs bytes.Buffer
	saved := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(saved)
	srv.Close()
	Send(TrackLost, "", "lost")
	Wait()
	if s := logs.String(); !strings.Contains(s, "Alert not delivered") || strings.Contains(s, "SECRET-TOKEN") {
		t.Errorf("unexpected log for a failed delivery: %s", s)
	}
	_ = Configure("")
}

func TestHeartbeat(t *testing.T) {
	saved := heartbeatEvery
	heartbeatEvery = 10 * time.Millisecond
	defer func() { heartbeatEvery = saved }()

	var pings atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			pings.Add(1)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	if err := Heartbeat(ctx, "ftp://nope"); err == nil {
		t.Error("an invalid heartbeat URL must be rejected")
	}
	if err := Heartbeat(ctx, ""); err != nil {
		t.Errorf("no heartbeat URL is valid: %v", err)
	}
	if err := Heartbeat(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); pings.Load() < 3; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("got %d pings, want at least 3", pings.Load())
		}
	}
	cancel()
	time.Sleep(50 * time.Millisecond) // let a ping in flight land
	stopped := pings.Load()
	time.Sleep(50 * time.Millisecond)
	if pings.Load() != stopped {
		t.Error("the heartbeat kept pinging after the shutdown")
	}
}
