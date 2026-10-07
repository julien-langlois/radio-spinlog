package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/config"
	"github.com/julien-langlois/radio-spinlog/internal/crawler"
	"github.com/julien-langlois/radio-spinlog/internal/domain"
	"github.com/julien-langlois/radio-spinlog/internal/storage"
)

// version is set at release time by GoReleaser (-ldflags "-X main.version=...")
var version = "dev"

func main() {
	// 1. Setup Structured Logging (Info by default, -debug for more)
	logLevel := new(slog.LevelVar)
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	// 2. Load Configurations (CLI -> Local -> Configs)
	radios, globalCfg := config.Load(version)
	if globalCfg.Debug {
		logLevel.Set(slog.LevelDebug)
	}
	if globalCfg.Stats {
		showStats(globalCfg.DBPath)
		return
	}
	if len(radios) == 0 {
		slog.Error("No active radios found. Check your config or CLI flags.")
		os.Exit(1)
	}

	// 3. Initialize the database (SQLite file or PostgreSQL URL)
	db, err := storage.Open(globalCfg.DBPath)
	if err != nil {
		slog.Error("Database init failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.UpsertRadios(radios); err != nil {
		slog.Error("Failed to record radios", "err", err)
		os.Exit(1)
	}

	// 4. Setup Context for Graceful Shutdown (cancelled on Ctrl+C / SIGTERM)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 5. Setup Pub/Sub Channel for Tracks
	trackChan := make(chan domain.Track, 100)
	writerDone := make(chan struct{})
	go func() {
		db.StartWriter(ctx, trackChan)
		close(writerDone)
	}()

	slog.Info("Starting radio-spinlog", "version", version, "active_radios", len(radios), "db", storage.Redact(globalCfg.DBPath))

	// 6. Initialize both scrapers
	htmlScraper := &crawler.StaticScraper{UserAgent: globalCfg.UserAgent}
	apiScraper := &crawler.ApiScraper{UserAgent: globalCfg.UserAgent}

	// 7. Route each radio to its scraper
	var workers sync.WaitGroup
	for _, r := range radios {
		var activeScraper crawler.Scraper = htmlScraper // HTML by default

		if r.Type == "api" {
			activeScraper = apiScraper
			slog.Debug("Using API scraper", "radio", r.Slug)
		} else {
			slog.Debug("Using HTML scraper", "radio", r.Slug)
		}

		lastKey := db.LastKey(r.Slug)
		workers.Go(func() {
			crawler.StartMonitoring(ctx, r, activeScraper, lastKey, trackChan)
		})
	}

	// 8. Wait for OS signals (Ctrl+C)
	<-ctx.Done()

	slog.Warn("Shutdown signal received, closing resources...")
	workers.Wait()   // No worker can send anymore
	close(trackChan) // Lets the writer drain what is buffered
	<-writerDone     // Everything is written before db.Close()
	slog.Info("Goodbye!")
}

// showStats prints the content of the archive every 2 minutes until Ctrl+C.
// It only reads the database, so it can run next to a crawling instance.
func showStats(dbPath string) {
	db, err := storage.Open(dbPath)
	if err != nil {
		slog.Error("Database init failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	for {
		fmt.Print("\033[H\033[2J") // clear the terminal
		if err := db.WriteStats(os.Stdout); err != nil {
			slog.Error("Cannot read stats", "err", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Minute):
		}
	}
}
