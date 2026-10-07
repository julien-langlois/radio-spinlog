package crawler

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/domain"
)

// Check fetches a radio once and prints what its selectors extract, without archiving anything:
// the way to validate a station definition. It reports whether a complete track was extracted.
func Check(ctx context.Context, w io.Writer, cfg domain.RadioConfig, scraper Scraper) bool {
	fmt.Fprintf(w, "%s  %s [%s, %s]\n", cfg.Slug, cfg.Name, cfg.Country, cfg.Timezone)
	fmt.Fprintf(w, "  source     %s  %s\n", cfg.Type, cfg.URL)

	track, err := scraper.Fetch(ctx, cfg)
	if err != nil {
		fmt.Fprintf(w, "  result     FAILED: %v\n", err)
		return false
	}
	fmt.Fprintf(w, "  artist     %s\n", track.Artist)
	fmt.Fprintf(w, "  title      %s\n", track.Title)

	result := "OK"
	switch {
	case track.Artist == "" && track.Title == "":
		result = "NOTHING EXTRACTED: ad break or silence at the source, or selectors that match nothing"
	case track.Artist == "" || track.Title == "":
		result = "INCOMPLETE: artist or title is empty"
	}

	switch {
	case cfg.Selectors.PlayedAt == "":
		fmt.Fprintln(w, "  played_at  (no selector: the same song twice in a row is counted once)")
	case track.PlayedAt.IsZero():
		fmt.Fprintln(w, "  played_at  NOT EXTRACTED")
		if result == "OK" {
			result = "INCOMPLETE: the played_at selector matches nothing or its format is not recognised"
		}
	default:
		local := track.PlayedAt
		if loc, err := time.LoadLocation(cfg.Timezone); err == nil {
			local = local.In(loc)
		}
		fmt.Fprintf(w, "  played_at  %s UTC = %s at the station\n", track.PlayedAtUTC(), local.Format("2006-01-02 15:04:05 MST"))
	}

	fmt.Fprintf(w, "  result     %s\n", result)
	return result == "OK"
}
