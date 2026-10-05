package crawler

import (
	"bytes"
	"context"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/domain"

	"github.com/PuerkitoBio/goquery"
)

// Scraper defines the contract for any crawling method
type Scraper interface {
	Fetch(ctx context.Context, cfg domain.RadioConfig) (*domain.Track, error)
}

// maxAge is how long a downloaded page is reused: slightly under the polling interval,
// so radios sharing a URL cause one request per interval instead of one each
func maxAge(cfg domain.RadioConfig) time.Duration {
	return time.Duration(cfg.Interval) * time.Second * 9 / 10
}

// StaticScraper uses standard HTTP and goquery
type StaticScraper struct {
	UserAgent string
}

// Fetch retrieves the HTML and parses it
func (s *StaticScraper) Fetch(ctx context.Context, cfg domain.RadioConfig) (*domain.Track, error) {
	body, err := fetch(ctx, cfg.URL, s.UserAgent, "", maxAge(cfg))
	if err != nil {
		return nil, err
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	artist := doc.Find(cfg.Selectors.Artist).First().Text()
	title := doc.Find(cfg.Selectors.Title).First().Text()

	var playedAt string
	if cfg.Selectors.PlayedAt != "" {
		playedAt = doc.Find(cfg.Selectors.PlayedAt).First().Text()
	}

	return &domain.Track{
		Artist:   artist,
		Title:    title,
		PlayedAt: parsePlayedAt(playedAt, cfg),
	}, nil
}
