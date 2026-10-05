package crawler

import (
	"context"

	"github.com/julien-langlois/radio-spinlog/internal/domain"

	"github.com/tidwall/gjson"
)

// ApiScraper fetches JSON data and extracts values using gjson paths.
type ApiScraper struct {
	UserAgent string
}

// Fetch retrieves the JSON from the API and parses it based on the configured selectors.
func (s *ApiScraper) Fetch(ctx context.Context, cfg domain.RadioConfig) (*domain.Track, error) {
	body, err := fetch(ctx, cfg.URL, s.UserAgent, "application/json", maxAge(cfg))
	if err != nil {
		return nil, err
	}

	// Extract data using GJSON dot notation (e.g., "0.currentTrack.artist")
	artist := gjson.GetBytes(body, cfg.Selectors.Artist).String()
	title := gjson.GetBytes(body, cfg.Selectors.Title).String()

	var playedAt string
	if cfg.Selectors.PlayedAt != "" {
		playedAt = gjson.GetBytes(body, cfg.Selectors.PlayedAt).String()
	}

	return &domain.Track{
		Artist:   artist,
		Title:    title,
		PlayedAt: parsePlayedAt(playedAt, cfg),
	}, nil
}
