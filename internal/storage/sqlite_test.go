package storage

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/julien-langlois/radio-spinlog/internal/domain"
)

// A replayed song must produce a new row: tracks is a play history, not a catalog.
func TestReplaysAreKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := NewSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Reopening an existing database must work (idempotent schema migration)
	db, err = NewSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var mode string
	if err := db.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
	}

	ch := make(chan domain.Track, 3)
	ch <- domain.Track{RadioSlug: "a", Artist: "X", Title: "Y", Hash: "h1"}
	ch <- domain.Track{RadioSlug: "b", Artist: "X", Title: "Y", Hash: "h1"}
	ch <- domain.Track{RadioSlug: "a", Artist: "X", Title: "Y", Hash: "h1", PlayedAt: time.Date(2026, 10, 5, 8, 50, 7, 0, time.UTC)}
	close(ch)
	db.StartWriter(ch)

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
