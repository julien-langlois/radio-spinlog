package crawler

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/domain"
)

// parsePlayedAt converts a source timestamp to UTC. Accepted: unix seconds or milliseconds,
// RFC 3339 (explicit offset), or a local "YYYY-MM-DD HH:MM:SS" read in the station's timezone.
// Returns the zero time when the value is empty or unreadable.
func parsePlayedAt(raw string, cfg domain.RadioConfig) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}

	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if n > 1e11 { // too large for seconds: milliseconds
			return time.UnixMilli(n).UTC()
		}
		return time.Unix(n, 0).UTC()
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC()
	}

	loc, err := time.LoadLocation(cfg.Timezone) // "" is UTC; validated at config load
	if err != nil {
		loc = time.UTC
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t.UTC()
		}
	}

	slog.Warn("Unreadable played_at, only scraped_at will be stored", "radio", cfg.Slug, "value", raw)
	return time.Time{}
}
