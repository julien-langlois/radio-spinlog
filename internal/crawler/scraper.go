package crawler

import (
	"bytes"
	"context"
	"strings"
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

	artist := text(doc, cfg.Selectors.Artist)
	title := text(doc, cfg.Selectors.Title)

	var playedAt string
	if cfg.Selectors.PlayedAt != "" {
		playedAt = text(doc, cfg.Selectors.PlayedAt)
	}

	return &domain.Track{
		Artist:   artist,
		Title:    title,
		PlayedAt: parsePlayedAt(playedAt, cfg),
	}, nil
}

// text returns the text of the first match with its whitespace collapsed, as a browser shows it:
// indentation and line breaks of the markup are not part of the value, and an element holding
// only whitespace (ad break) is empty
func text(doc *goquery.Document, selector string) string {
	return strings.Join(strings.Fields(doc.Find(selector).First().Text()), " ")
}
