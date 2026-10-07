package crawler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/alert"
	"github.com/julien-langlois/radio-spinlog/internal/domain"
)

// fakeScraper replays a scripted sequence of results, then stops the worker
type fakeScraper struct {
	steps []func() (*domain.Track, error)
	stop  context.CancelFunc
}

func (f *fakeScraper) Fetch(ctx context.Context, cfg domain.RadioConfig) (*domain.Track, error) {
	if len(f.steps) == 0 {
		f.stop()
		return nil, ctx.Err()
	}
	step := f.steps[0]
	f.steps = f.steps[1:]
	return step()
}

func play(artist, title string, at time.Time) func() (*domain.Track, error) {
	return func() (*domain.Track, error) {
		return &domain.Track{Artist: artist, Title: title, PlayedAt: at}, nil
	}
}

func TestWorkerDetectsPlays(t *testing.T) {
	startJitter = 1 // no start delay; Interval 0 below makes the loop immediate
	var unknown time.Time
	t1 := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	t2 := t1.Add(3 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	scraper := &fakeScraper{stop: cancel, steps: []func() (*domain.Track, error){
		play("Seed", "Song", unknown), // already archived before the restart: skipped
		play("A", "One", unknown),
		play("a ", " ONE", unknown), // same song, different case and spacing: still playing
		play("", "", unknown),       // ad break
		func() (*domain.Track, error) { return nil, errors.New("boom") },
		play("B", "Two", unknown),
		play("A", "One", unknown), // replay later: a new play
		play("C", "Three", t1),
		play("C", "Three", t1), // same start time: still playing
		play("C", "Three", t2), // same song, new start time: played twice in a row
	}}

	seed := domain.Track{Hash: GenerateFingerprint("Seed", "Song")}
	ch := make(chan domain.Track, 20)
	StartMonitoring(ctx, domain.RadioConfig{Slug: "r"}, scraper, seed.PlayKey(), ch)
	close(ch)

	var got []string
	for tr := range ch {
		if tr.RadioSlug != "r" || tr.Hash == "" {
			t.Errorf("track not filled in: %+v", tr)
		}
		got = append(got, tr.Title+"@"+tr.PlayedAtUTC())
	}
	want := []string{"One@", "Two@", "One@", "Three@2026-10-05 08:00:00", "Three@2026-10-05 08:03:00"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// A radio that goes quiet raises one alert, however long it lasts, then one when it comes back.
func TestWorkerAlertsOnStaleSource(t *testing.T) {
	var mu sync.Mutex
	var events []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]string
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		mu.Lock()
		events = append(events, p["event"]+"|"+p["radio"])
		mu.Unlock()
	}))
	defer srv.Close()
	if err := alert.Configure(srv.URL); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = alert.Configure("") }()

	startJitter = 1
	savedStale := staleAfter
	staleAfter = 0 // every poll without a new track counts as a long silence
	defer func() { staleAfter = savedStale }()

	var unknown time.Time
	ctx, cancel := context.WithCancel(context.Background())
	scraper := &fakeScraper{stop: cancel, steps: []func() (*domain.Track, error){
		play("", "", unknown), // silence: stale
		play("", "", unknown), // still stale: logged again, not alerted again
		play("A", "One", unknown),
	}}
	ch := make(chan domain.Track, 5)
	StartMonitoring(ctx, domain.RadioConfig{Slug: "fr-r"}, scraper, "", ch)
	alert.Wait()

	sort.Strings(events)
	if want := "source_recovered|fr-r source_stale|fr-r"; strings.Join(events, " ") != want {
		t.Errorf("got %v, want %s", events, want)
	}
}
