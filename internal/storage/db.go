package storage

import (
	"context"
	"database/sql"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/alert"
	"github.com/julien-langlois/radio-spinlog/internal/domain"

	_ "github.com/jackc/pgx/v5/stdlib" // pure Go PostgreSQL driver (Supabase or any other PostgreSQL)
	_ "modernc.org/sqlite"             // pure Go driver: no C compiler needed, cross-compiles everywhere
)

// DB is the archive: a local SQLite file, or a PostgreSQL database when the target is a postgres:// URL.
// The backend is chosen once at startup; there is no failover from one to the other.
type DB struct {
	db *sql.DB
	pg bool
}

// insertRetryDelays spreads the retries of a failed insert over about a minute: enough for a
// dropped connection or a pooler restart, short enough to keep scraped_at meaningful.
var insertRetryDelays = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second}

const sqliteSchema = `
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

// Same tables as SQLite. TIMESTAMP(0) without time zone holds UTC at second precision, so both
// backends render YYYY-MM-DD HH:MM:SS. Index names are prefixed because they are schema-wide in
// PostgreSQL. Row level security keeps the tables out of an auto-generated REST API (Supabase
// exposes the public schema); the owner role used by the daemon is not affected.
const postgresSchema = `
	CREATE TABLE IF NOT EXISTS tracks (
		id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		radio_slug TEXT NOT NULL,
		artist TEXT NOT NULL,
		title TEXT NOT NULL,
		hash TEXT NOT NULL,
		scraped_at TIMESTAMP(0) DEFAULT (now() AT TIME ZONE 'utc'),
		played_at TIMESTAMP(0)
	);
	CREATE TABLE IF NOT EXISTS radios (
		slug TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		country TEXT NOT NULL,
		timezone TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_tracks_radio_time ON tracks(radio_slug, scraped_at);
	CREATE INDEX IF NOT EXISTS idx_tracks_hash ON tracks(hash);
	ALTER TABLE tracks ENABLE ROW LEVEL SECURITY;
	ALTER TABLE radios ENABLE ROW LEVEL SECURITY;
`

func isPostgres(target string) bool {
	return strings.HasPrefix(target, "postgres://") || strings.HasPrefix(target, "postgresql://")
}

// Redact makes a database target safe to log: a PostgreSQL URL loses its password and its
// query string (which may carry one too), a file path is returned as is.
func Redact(target string) string {
	if !isPostgres(target) {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return "postgres://<invalid URL>"
	}
	u.RawQuery = ""
	return u.Redacted()
}

// Open initializes the archive: PostgreSQL for a postgres:// or postgresql:// URL, otherwise a
// SQLite file in WAL mode.
// tracks is a play history: one row per broadcast, the same song appears as often as it is played.
func Open(target string) (*DB, error) {
	if isPostgres(target) {
		return openPostgres(target)
	}
	return openSQLite(target)
}

func openSQLite(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return nil, err
	}

	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, err
	}
	// Databases created before played_at existed
	if _, err := db.Exec(`ALTER TABLE tracks ADD COLUMN played_at DATETIME`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, err
	}

	return &DB{db: db}, nil
}

func openPostgres(dsn string) (*DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	// Fail fast on an unreachable server instead of waiting for the TCP timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, postgresSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db, pg: true}, nil
}

// q adapts the ? placeholders of a query to the backend ($1, $2... for PostgreSQL)
func (s *DB) q(query string) string {
	if !s.pg {
		return query
	}
	for n := 1; strings.Contains(query, "?"); n++ {
		query = strings.Replace(query, "?", "$"+strconv.Itoa(n), 1)
	}
	return query
}

// timeText renders a timestamp expression as YYYY-MM-DD HH:MM:SS (the format of Track.PlayedAtUTC).
// SQLite stores that text already; PostgreSQL needs to_char to be independent of the server's DateStyle.
func (s *DB) timeText(expr string) string {
	if !s.pg {
		return expr
	}
	return "to_char(" + expr + ", 'YYYY-MM-DD HH24:MI:SS')"
}

// UpsertRadios records the station metadata (country, timezone) that tracks.radio_slug points to.
// All timestamps in tracks are UTC; radios.timezone gives the station's local time when needed.
func (s *DB) UpsertRadios(radios []domain.RadioConfig) error {
	for _, r := range radios {
		_, err := s.db.Exec(s.q(`
			INSERT INTO radios (slug, name, country, timezone) VALUES (?, ?, ?, ?)
			ON CONFLICT(slug) DO UPDATE SET name = excluded.name, country = excluded.country, timezone = excluded.timezone
		`), r.Slug, r.Name, r.Country, r.Timezone)
		if err != nil {
			return err
		}
	}
	return nil
}

// LastKey returns the PlayKey of the radio's latest play if it was archived
// in the last 15 minutes, so a restart does not re-insert the song still on air.
func (s *DB) LastKey(radioSlug string) string {
	recent := `datetime('now', '-15 minutes')`
	if s.pg {
		recent = `(now() AT TIME ZONE 'utc') - interval '15 minutes'`
	}
	query := `
		SELECT hash || '|' || COALESCE(` + s.timeText("played_at") + `, '') FROM tracks
		WHERE radio_slug = ? AND scraped_at > ` + recent + `
		ORDER BY id DESC LIMIT 1`
	var key string
	err := s.db.QueryRow(s.q(query), radioSlug).Scan(&key)
	if err != nil && err != sql.ErrNoRows {
		slog.Error("Failed to read last track", "radio", radioSlug, "err", err)
	}
	return key
}

// StartWriter listens to the channel and writes to DB safely.
// A failed insert is retried (see insertRetryDelays) before the track is given up; once ctx is
// cancelled it gets a single last attempt, so a shutdown never waits on an unreachable database.
func (s *DB) StartWriter(ctx context.Context, trackChan <-chan domain.Track) {
	slog.Info("Database writer started")

	stmt, err := s.db.Prepare(s.q(`
		INSERT INTO tracks (radio_slug, artist, title, hash, played_at)
		VALUES (?, ?, ?, ?, ?)
	`))
	if err != nil {
		slog.Error("Failed to prepare statement", "err", err)
		alert.Send(alert.WriterFailed, "", "the database writer could not start, nothing is being archived")
		return
	}
	defer stmt.Close()

	for track := range trackChan {
		var playedAt any // NULL when the source gives no start time
		if p := track.PlayedAtUTC(); p != "" {
			playedAt = p
		}
		// ponytail: every error is retried, so a permanent one (constraint, dropped table) holds the
		// writer for a minute per track, and an insert whose acknowledgement was lost is written
		// twice; classify errors if either ever shows up
		for attempt := 0; ; attempt++ {
			_, err := stmt.Exec(track.RadioSlug, track.Artist, track.Title, track.Hash, playedAt)
			if err == nil {
				break
			}
			if attempt == len(insertRetryDelays) || ctx.Err() != nil {
				slog.Error("Insert failed, track lost", "radio", track.RadioSlug, "artist", track.Artist, "title", track.Title, "err", err)
				alert.Send(alert.TrackLost, "", "tracks are being lost, the database rejects writes (first one: "+track.RadioSlug+", "+track.Artist+" - "+track.Title+")")
				break
			}
			slog.Warn("Insert failed, retrying", "radio", track.RadioSlug, "retry_in", insertRetryDelays[attempt].String(), "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(insertRetryDelays[attempt]):
			}
		}
	}
	slog.Info("Database writer stopped cleanly")
}

func (s *DB) Close() {
	s.db.Close()
}
