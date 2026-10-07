// Package alert pushes the few events that need a human (a source gone quiet, tracks being
// lost) to a webhook. Everything is also logged by the caller: without a webhook, alerts are
// simply log lines. It also pings a heartbeat URL, for the one failure a webhook cannot report:
// a daemon that is no longer running.
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Events sent to the webhook, under the "event" key
const (
	Started         = "started"          // the daemon started: also proves the webhook works
	SourceStale     = "source_stale"     // a radio produced no new track for a long time
	SourceRecovered = "source_recovered" // a stale radio produces tracks again
	TrackLost       = "track_lost"       // a track could not be written to the database
	WriterFailed    = "writer_failed"    // the database writer could not start: nothing is archived
)

var (
	// heartbeatEvery is the ping period; the monitoring service must tolerate at least twice that
	heartbeatEvery = 5 * time.Minute

	// cooldown mutes repeats of the same event for the same radio, so an outage is one alert, not a flood
	cooldown = time.Hour

	client = &http.Client{Timeout: 10 * time.Second}

	mu       sync.Mutex
	endpoint string
	lastSent = map[string]time.Time{}
	pending  sync.WaitGroup
)

func checkURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("not an http(s) URL")
	}
	return nil
}

// Configure sets the webhook URL; an empty one disables alerts. Call it once at startup.
// The URL usually embeds a token: it is never logged, not even in errors.
func Configure(rawURL string) error {
	if rawURL != "" {
		if err := checkURL(rawURL); err != nil {
			return err
		}
	}
	mu.Lock()
	defer mu.Unlock()
	endpoint = rawURL
	lastSent = map[string]time.Time{}
	return nil
}

// Send posts an event to the webhook in the background: it never blocks or fails the caller.
// radio may be empty for events that are not about one station.
func Send(event, radio, text string) {
	mu.Lock()
	target, key := endpoint, event+"|"+radio
	muted := target == "" || time.Since(lastSent[key]) < cooldown
	if !muted {
		lastSent[key] = time.Now()
	}
	mu.Unlock()
	if muted {
		return
	}

	// "text" is read by Slack-compatible webhooks, "content" by Discord; the others get plain JSON
	text = "radio-spinlog: " + text
	body, _ := json.Marshal(map[string]string{"event": event, "radio": radio, "text": text, "content": text})
	pending.Go(func() {
		if err := request(http.MethodPost, target, body); err != nil {
			slog.Error("Alert not delivered", "event", event, "radio", radio, "err", err)
		}
	})
}

// Heartbeat pings rawURL with a GET now and every heartbeatEvery until ctx is cancelled, so an
// external monitor (healthchecks.io and the like) notices when the daemon stops. An empty URL
// does nothing. Like the webhook, the URL is a secret and is never logged.
func Heartbeat(ctx context.Context, rawURL string) error {
	if rawURL == "" {
		return nil
	}
	if err := checkURL(rawURL); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(heartbeatEvery)
		defer ticker.Stop()
		for {
			if err := request(http.MethodGet, rawURL, nil); err != nil {
				slog.Warn("Heartbeat not delivered", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func request(method, target string, body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid URL")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		// A *url.Error prints the URL, token included: keep only the cause
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("server answered %s", res.Status)
	}
	return nil
}

// Wait blocks until the alerts already sent are delivered or timed out; call it before exiting.
func Wait() {
	pending.Wait()
}
