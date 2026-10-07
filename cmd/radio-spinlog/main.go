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

	"github.com/julien-langlois/radio-spinlog/internal/alert"
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

	// Scrapers: HTML by default, JSON for the "api" type
	htmlScraper := &crawler.StaticScraper{UserAgent: globalCfg.UserAgent}
	apiScraper := &crawler.ApiScraper{UserAgent: globalCfg.UserAgent}
	scraperFor := func(r domain.RadioConfig) crawler.Scraper {
		if r.Type == "api" {
			return apiScraper
		}
		return htmlScraper
	}

	if globalCfg.Check {
		// One request per URL, no database, no alert: safe next to a running daemon
		ok := true
		for _, r := range radios {
			ok = crawler.Check(context.Background(), os.Stdout, r, scraperFor(r)) && ok
		}
		if !ok {
			os.Exit(1)
		}
		return
	}

	if err := alert.Configure(globalCfg.Webhook); err != nil {
		slog.Error("Invalid -webhook / RADIO_SPINLOG_WEBHOOK", "err", err)
		os.Exit(1)
	}
	defer alert.Wait() // runs last: alerts raised during the shutdown still go out

	// 3. Initialize the database (SQLite file or PostgreSQL URL)
	db, err := storage.Open(globalCfg.DBPath)
	if err != nil {
		slog.Error("Database init failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	// A station is archived by one collector at a time, or every play would be written twice
	radios, err = db.LockRadios(radios)
	if err != nil {
		slog.Error("Cannot reserve the radios in the database", "err", err)
		os.Exit(1)
	}
	if len(radios) == 0 {
		slog.Error("Every requested radio is already archived by another collector on this database, nothing to do")
		os.Exit(1)
	}
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

	slog.Info("Starting radio-spinlog", "version", version, "active_radios", len(radios), "db", storage.Redact(globalCfg.DBPath), "alerts", globalCfg.Webhook != "")
	alert.Send(alert.Started, "", fmt.Sprintf("%s started, tracking %d stations", version, len(radios)))

	// 6. One worker per radio
	var workers sync.WaitGroup
	for _, r := range radios {
		lastKey := db.LastKey(r.Slug)
		workers.Go(func() {
			crawler.StartMonitoring(ctx, r, scraperFor(r), lastKey, trackChan)
		})
	}

	// 7. Wait for OS signals (Ctrl+C)
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
