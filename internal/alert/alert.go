// Package alert pushes the few events that need a human (a source gone quiet, tracks being
// lost) to a webhook. Everything is also logged by the caller: without a webhook, alerts are
// simply log lines.
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
	// cooldown mutes repeats of the same event for the same radio, so an outage is one alert, not a flood
	cooldown = time.Hour

	client = &http.Client{Timeout: 10 * time.Second}

	mu       sync.Mutex
	endpoint string
	lastSent = map[string]time.Time{}
	pending  sync.WaitGroup
)

// Configure sets the webhook URL; an empty one disables alerts. Call it once at startup.
// The URL usually embeds a token: it is never logged, not even in errors.
func Configure(rawURL string) error {
	if rawURL != "" {
		u, err := url.Parse(rawURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("webhook must be an http(s) URL")
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
		if err := post(target, body); err != nil {
			slog.Error("Alert not delivered", "event", event, "radio", radio, "err", err)
		}
	})
}

func post(target string, body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
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
		return fmt.Errorf("webhook answered %s", res.Status)
	}
	return nil
}

// Wait blocks until the alerts already sent are delivered or timed out; call it before exiting.
func Wait() {
	pending.Wait()
}
