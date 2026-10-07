package crawler

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/alert"
	"github.com/julien-langlois/radio-spinlog/internal/domain"
)

var (
	// startJitter spreads the first poll of each radio so sources are not all hit in the same second
	startJitter = 10 * time.Second
	// staleAfter is how long a radio may go without a new track before a warning is logged
	staleAfter = time.Hour
)

// StartMonitoring handles the infinite loop for a specific radio.
// lastKey is the PlayKey of the broadcast already archived as "now playing" (empty if none).
func StartMonitoring(ctx context.Context, cfg domain.RadioConfig, scraper Scraper, lastKey string, trackChan chan<- domain.Track) {
	baseInterval := time.Duration(cfg.Interval) * time.Second
	backoffDelay := time.Duration(rand.Int63n(int64(startJitter)))

	// Stale-source detection: errors are already logged, but a broken selector or a frozen
	// endpoint fails silently (empty or unchanged data), so watch for the absence of new tracks
	lastNewTrack := time.Now()
	var lastStaleWarning time.Time

	for {
		select {
		case <-ctx.Done():
			slog.Info("Stopping worker", "radio", cfg.Slug)
			return

		case <-time.After(backoffDelay):
			if time.Since(lastNewTrack) > staleAfter && time.Since(lastStaleWarning) > staleAfter {
				since := time.Since(lastNewTrack).Round(time.Minute).String()
				slog.Warn("No new track for a long time, source or selectors may be broken", "radio", cfg.Slug, "since", since)
				if lastStaleWarning.IsZero() { // the log line repeats, the alert is sent once per outage
					alert.Send(alert.SourceStale, cfg.Slug, cfg.Slug+" has produced no new track for "+since+": its source or its selectors may be broken")
				}
				lastStaleWarning = time.Now()
			}

			slog.Debug("Fetching data...", "radio", cfg.Slug, "type", cfg.Type)

			track, err := scraper.Fetch(ctx, cfg)

			// 1. Handle Errors (Exponential Backoff)
			if err != nil {
				if backoffDelay < baseInterval {
					backoffDelay = baseInterval
				}
				backoffDelay *= 2
				if backoffDelay > 5*time.Minute {
					backoffDelay = 5 * time.Minute
				}
				slog.Error("Scraping failed, backing off", "radio", cfg.Slug, "err", err, "delay", backoffDelay)
				continue
			}

			// Reset backoff on success
			backoffDelay = baseInterval

			// 2. Handle empty results (e.g., commercials)
			if track.Artist == "" && track.Title == "" {
				slog.Debug("Empty track data (commercial or silence)", "radio", cfg.Slug)
				continue
			}

			// 3. Debouncing: a play is its fingerprint plus, when the source gives it, its start time
			// ponytail: without a played_at selector, the same song twice in a row (or around an
			// ad break) counts as one play; configure played_at when the source exposes it.
			track.RadioSlug = cfg.Slug
			track.Hash = GenerateFingerprint(track.Artist, track.Title)
			key := track.PlayKey()
			if key == lastKey {
				continue // Song is still playing
			}
			lastKey = key

			if !lastStaleWarning.IsZero() {
				slog.Info("Source is producing tracks again", "radio", cfg.Slug)
				alert.Send(alert.SourceRecovered, cfg.Slug, cfg.Slug+" is producing tracks again")
				lastStaleWarning = time.Time{}
			}
			lastNewTrack = time.Now()

			// Send to the database writer
			trackChan <- *track
			slog.Info("New track detected", "radio", cfg.Slug, "artist", track.Artist, "title", track.Title)
		}
	}
}
