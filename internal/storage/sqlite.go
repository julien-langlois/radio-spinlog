package storage

import (
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/julien-langlois/radio-spinlog/internal/domain"

	_ "modernc.org/sqlite" // pure Go driver: no C compiler needed, cross-compiles everywhere
)

type SQLiteDB struct {
	db *sql.DB
}

// NewSQLite initializes the database with WAL mode.
// tracks is a play history: one row per broadcast, the same song appears as often as it is played.
func NewSQLite(dbPath string) (*SQLiteDB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return nil, err
	}

	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	query := `
	CREATE TABLE IF NOT EXISTS tracks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		radio_slug TEXT NOT NULL,
		artist TEXT NOT NULL,
		title TEXT NOT NULL,
		hash TEXT NOT NULL,
		scraped_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		played_at DATETIME
	);
	CREATE TABLE IF NOT EXISTS radios (
		slug TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		country TEXT NOT NULL,
		timezone TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_radio_time ON tracks(radio_slug, scraped_at);
	CREATE INDEX IF NOT EXISTS idx_hash ON tracks(hash);
	`
	if _, err := db.Exec(query); err != nil {
		return nil, err
	}
	// Databases created before played_at existed
	if _, err := db.Exec(`ALTER TABLE tracks ADD COLUMN played_at DATETIME`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return nil, err
	}

	return &SQLiteDB{db: db}, nil
}

// UpsertRadios records the station metadata (country, timezone) that tracks.radio_slug points to.
// All timestamps in tracks are UTC; radios.timezone gives the station's local time when needed.
func (s *SQLiteDB) UpsertRadios(radios []domain.RadioConfig) error {
	for _, r := range radios {
		_, err := s.db.Exec(`
			INSERT INTO radios (slug, name, country, timezone) VALUES (?, ?, ?, ?)
			ON CONFLICT(slug) DO UPDATE SET name = excluded.name, country = excluded.country, timezone = excluded.timezone
		`, r.Slug, r.Name, r.Country, r.Timezone)
		if err != nil {
			return err
		}
	}
	return nil
}

// LastKey returns the PlayKey of the radio's latest play if it was archived
// in the last 15 minutes, so a restart does not re-insert the song still on air.
func (s *SQLiteDB) LastKey(radioSlug string) string {
	var hash string
	err := s.db.QueryRow(`
		SELECT hash || '|' || COALESCE(played_at, '') FROM tracks
		WHERE radio_slug = ? AND scraped_at > datetime('now', '-15 minutes')
		ORDER BY id DESC LIMIT 1
	`, radioSlug).Scan(&hash)
	if err != nil && err != sql.ErrNoRows {
		slog.Error("Failed to read last track", "radio", radioSlug, "err", err)
	}
	return hash
}

// StartWriter listens to the channel and writes to DB safely
func (s *SQLiteDB) StartWriter(trackChan <-chan domain.Track) {
	slog.Info("SQLite writer started")

	stmt, err := s.db.Prepare(`
		INSERT INTO tracks (radio_slug, artist, title, hash, played_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''))
	`)
	if err != nil {
		slog.Error("Failed to prepare statement", "err", err)
		return
	}
	defer stmt.Close()

	for track := range trackChan {
		_, err := stmt.Exec(track.RadioSlug, track.Artist, track.Title, track.Hash, track.PlayedAtUTC())
		if err != nil {
			slog.Error("Insert error", "err", err)
		}
	}
	slog.Info("SQLite writer stopped cleanly")
}

func (s *SQLiteDB) Close() {
	s.db.Close()
}
