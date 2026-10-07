package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/julien-langlois/radio-spinlog/configs"
)

func TestLoadRadiosValidatesAndFilters(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("fr/radios.json", `{"timezone": "Europe/Paris", "radios": [
		{"slug": "fr-ok", "url": "http://x", "interval_seconds": 120},
		{"slug": "fr-tokyo", "url": "http://x", "interval_seconds": 120, "timezone": "Asia/Tokyo"},
		{"slug": "fr-ok", "url": "http://dup", "interval_seconds": 120},
		{"slug": "fr-no-interval", "url": "http://x"},
		{"slug": "fr-too-fast", "url": "http://x", "interval_seconds": 5},
		{"slug": "fr-bad-tz", "url": "http://x", "interval_seconds": 120, "timezone": "Mars/Olympus"},
		{"slug": "fr-no-url", "interval_seconds": 120},
		{"slug": "no-country", "url": "http://x", "interval_seconds": 120},
		{"slug": "us-other-country", "url": "http://x", "interval_seconds": 120},
		{"slug": "fr-", "url": "http://x", "interval_seconds": 120},
		{"url": "http://x", "interval_seconds": 120}
	]}`)
	write("us/radios.json", `{not json`)

	radios := loadRadios(os.DirFS(dir))
	if len(radios) != 2 {
		t.Fatalf("got %d radios, want 2: %+v", len(radios), radios)
	}
	if r := radios[0]; r.Slug != "fr-ok" || r.URL != "http://x" || r.Country != "fr" || r.Timezone != "Europe/Paris" {
		t.Errorf("unexpected first radio: %+v", r)
	}
	if r := radios[1]; r.Slug != "fr-tokyo" || r.Timezone != "Asia/Tokyo" {
		t.Errorf("unexpected second radio: %+v", r)
	}

	// The stations shipped in the binary must all be valid
	builtin := loadRadios(configs.FS)
	if len(builtin) == 0 {
		t.Error("no built-in station loaded")
	}
	for _, r := range builtin {
		if r.Country == "" || r.Country == "." {
			t.Errorf("built-in radio %q has no country", r.Slug)
		}
	}

	got := filterRadios(radios, []string{" fr-tokyo", "unknown", "", "fr-tokyo", "fr-ok "})
	if len(got) != 2 || got[0].Slug != "fr-tokyo" || got[1].Slug != "fr-ok" {
		t.Errorf("filterRadios = %+v", got)
	}
}
