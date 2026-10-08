package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/domain"
)

func TestParsePlayedAt(t *testing.T) {
	paris := domain.RadioConfig{Timezone: "Europe/Paris"}
	cases := []struct {
		raw  string
		cfg  domain.RadioConfig
		want string
	}{
		{"2026-10-05 10:50:07", paris, "2026-10-05 08:50:07"},                // summer time, UTC+2
		{"2026-12-05 10:50:07", paris, "2026-12-05 09:50:07"},                // winter time, UTC+1
		{"2026-10-05T10:50:07-04:00", paris, "2026-10-05 14:50:07"},          // explicit offset wins
		{"1791190207", paris, "2026-10-05 08:50:07"},                         // unix seconds
		{"1791190207000", paris, "2026-10-05 08:50:07"},                      // unix milliseconds
		{"2026-10-05 10:50:07", domain.RadioConfig{}, "2026-10-05 10:50:07"}, // no timezone: UTC
		{"yesterday", paris, ""},
		{"", paris, ""},
	}
	for _, c := range cases {
		got := domain.Track{PlayedAt: parsePlayedAt(c.raw, c.cfg)}.PlayedAtUTC()
		if got != c.want {
			t.Errorf("parsePlayedAt(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestClockTime(t *testing.T) {
	paris, _ := time.LoadLocation("Europe/Paris")
	cases := []struct{ clock, now, want string }{
		{"14:03", "2026-10-08 14:05:00", "2026-10-08 12:03:00"}, // same day
		{"23:58", "2026-10-09 00:01:00", "2026-10-08 21:58:00"}, // started before midnight
		{"00:01", "2026-10-08 23:59:30", "2026-10-08 22:01:00"}, // source clock slightly ahead
	}
	for _, c := range cases {
		clock, _ := time.Parse("15:04", c.clock)
		now, _ := time.ParseInLocation("2006-01-02 15:04:05", c.now, paris)
		if got := clockTime(clock, now).Format("2006-01-02 15:04:05"); got != c.want {
			t.Errorf("clockTime(%s at %s) = %s, want %s", c.clock, c.now, got, c.want)
		}
	}
}

func TestStaticScraperCleansWhitespace(t *testing.T) {
	pages := map[string]string{
		"/track": "<div>\n  <span class=\"artist\">\r\n   Daft\n Punk\n  </span><span class=\"title\">  Da <b>Funk</b> </span></div>",
		"/ad":    "<div><span class=\"artist\">\n  </span><span class=\"title\"> </span></div>",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(pages[r.URL.Path]))
	}))
	defer srv.Close()

	fetchTrack := func(path string) *domain.Track {
		cfg := domain.RadioConfig{URL: srv.URL + path, Interval: 60, Selectors: domain.Selector{Artist: ".artist", Title: ".title"}}
		track, err := (&StaticScraper{UserAgent: "test"}).Fetch(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		return track
	}
	if track := fetchTrack("/track"); track.Artist != "Daft Punk" || track.Title != "Da Funk" {
		t.Errorf("got %q / %q, want Daft Punk / Da Funk", track.Artist, track.Title)
	}
	// Whitespace-only elements must read as empty, or the worker archives a blank play
	if track := fetchTrack("/ad"); track.Artist != "" || track.Title != "" {
		t.Errorf("ad break: got %q / %q, want empty", track.Artist, track.Title)
	}
}
