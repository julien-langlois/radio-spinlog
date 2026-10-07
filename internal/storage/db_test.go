package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/domain"
)

// postgresTarget returns a URL pointing at a fresh schema of the PostgreSQL server given by
// RADIO_SPINLOG_TEST_POSTGRES, dropped at the end of the test. Existing tables are never touched.
func postgresTarget(t *testing.T) string {
	dsn := os.Getenv("RADIO_SPINLOG_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("RADIO_SPINLOG_TEST_POSTGRES is not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("spinlog_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// A replayed song must produce a new row: tracks is a play history, not a catalog.
// Both backends must behave the same, down to the text of the play key.
func TestReplaysAreKept(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		testReplaysAreKept(t, filepath.Join(t.TempDir(), "test.db"))
	})
	t.Run("postgres", func(t *testing.T) {
		testReplaysAreKept(t, postgresTarget(t))
	})
}

func testReplaysAreKept(t *testing.T, target string) {
	db, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Reopening an existing database must work (idempotent schema migration)
	db, err = Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if !db.pg {
		var mode string
		if err := db.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
		}
	}

	// Recording the same radio twice updates it
	for _, name := range []string{"A", "A2"} {
		if err := db.UpsertRadios([]domain.RadioConfig{{Slug: "a", Name: name, Country: "fr", Timezone: "Europe/Paris"}}); err != nil {
			t.Fatal(err)
		}
	}
	var name string
	if err := db.db.QueryRow(`SELECT name FROM radios`).Scan(&name); err != nil || name != "A2" {
		t.Fatalf("radio name = %q, %v; want A2", name, err)
	}

	ch := make(chan domain.Track, 3)
	ch <- domain.Track{RadioSlug: "a", Artist: "X", Title: "Y", Hash: "h1"}
	ch <- domain.Track{RadioSlug: "b", Artist: "X", Title: "Y", Hash: "h1"}
	ch <- domain.Track{RadioSlug: "a", Artist: "X", Title: "Y", Hash: "h1", PlayedAt: time.Date(2026, 10, 5, 8, 50, 7, 0, time.UTC)}
	close(ch)
	db.StartWriter(context.Background(), ch)

	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE hash = 'h1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("got %d rows, want 3", n)
	}
	if got := db.LastKey("a"); got != "h1|2026-10-05 08:50:07" {
		t.Fatalf("LastKey(a) = %q", got)
	}
	if got := db.LastKey("b"); got != "h1|" {
		t.Fatalf("LastKey(b) = %q", got)
	}
	var out strings.Builder
	if err := db.WriteStats(&out); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "2026-10-05 08:50:07  a") || !strings.Contains(s, "a      2 ") {
		t.Fatalf("unexpected stats output:\n%s", s)
	}
	if got := db.LastKey("unknown"); got != "" {
		t.Fatalf("LastKey(unknown) = %q, want empty", got)
	}
}

// A database that comes back within the retry window must not cost a track; one that stays down
// costs the track but never blocks a shutdown.
func TestInsertIsRetried(t *testing.T) {
	saved := insertRetryDelays
	insertRetryDelays = []time.Duration{20 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond}
	defer func() { insertRetryDelays = saved }()

	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	track := domain.Track{RadioSlug: "a", Artist: "X", Title: "Y", Hash: "h1"}
	count := func() (n int) {
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM tracks`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// startWriter returns once the writer has archived a first track, then breaks the database
	startWriter := func(ctx context.Context, want int) (chan<- domain.Track, <-chan struct{}) {
		ch := make(chan domain.Track, 1)
		done := make(chan struct{})
		go func() {
			db.StartWriter(ctx, ch)
			close(done)
		}()
		ch <- track
		for deadline := time.Now().Add(5 * time.Second); count() != want; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("writer did not archive its first track (%d rows, want %d)", count(), want)
			}
		}
		if _, err := db.db.Exec(`ALTER TABLE tracks RENAME TO tracks_away`); err != nil {
			t.Fatal(err)
		}
		return ch, done
	}
	repair := func() {
		if _, err := db.db.Exec(`ALTER TABLE tracks_away RENAME TO tracks`); err != nil {
			t.Fatal(err)
		}
	}

	// Outage shorter than the retries: the track is written
	ch, done := startWriter(context.Background(), 1)
	ch <- track
	time.Sleep(30 * time.Millisecond)
	repair()
	close(ch)
	<-done
	if n := count(); n != 2 {
		t.Fatalf("got %d rows after a short outage, want 2", n)
	}

	// Shutdown during an outage: a single attempt, no waiting
	insertRetryDelays = []time.Duration{time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, done = startWriter(ctx, 3)
	ch <- track
	close(ch)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer still waiting on a broken database after shutdown")
	}
	repair()
	if n := count(); n != 3 {
		t.Fatalf("got %d rows, want 3 (the track sent during the outage is lost)", n)
	}
}
