package crawler

import (
	"testing"

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
