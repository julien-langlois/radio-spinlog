package crawler

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/domain"
)

// parsePlayedAt converts a source timestamp to UTC. Accepted: unix seconds or milliseconds,
// RFC 3339 (explicit offset), a local "YYYY-MM-DD HH:MM:SS" read in the station's timezone, or
// a bare local clock time ("HH:MM" or "HH:MM:SS", see clockTime).
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
	for _, layout := range []string{"15:04", "15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return clockTime(t, time.Now().In(loc))
		}
	}

	slog.Warn("Unreadable played_at, only scraped_at will be stored", "radio", cfg.Slug, "value", raw)
	return time.Time{}
}

// clockTime dates a bare clock time: of yesterday, today and tomorrow at the station, the day
// that puts it closest to now. A track started at 23:58 and read at 00:01 belongs to yesterday.
// ponytail: the date is a guess. A frozen page showing the same time for days yields one phantom
// play per day (the guessed date moves on); a source giving a full date has no such issue.
func clockTime(clock, now time.Time) time.Time {
	at := func(dayOffset int) time.Time {
		return time.Date(now.Year(), now.Month(), now.Day()+dayOffset, clock.Hour(), clock.Minute(), clock.Second(), 0, now.Location())
	}
	best := at(0)
	for _, t := range []time.Time{at(-1), at(1)} {
		if t.Sub(now).Abs() < best.Sub(now).Abs() {
			best = t
		}
	}
	return best.UTC()
}
