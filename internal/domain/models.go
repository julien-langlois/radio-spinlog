package domain

import "time"

// Track represents a single song played on a radio
type Track struct {
	RadioSlug string
	Artist    string
	Title     string
	Hash      string
	PlayedAt  time.Time // broadcast start given by the source, zero if unknown
}

// PlayedAtUTC formats the broadcast start like SQLite's CURRENT_TIMESTAMP (UTC), "" if unknown
func (t Track) PlayedAtUTC() string {
	if t.PlayedAt.IsZero() {
		return ""
	}
	return t.PlayedAt.UTC().Format("2006-01-02 15:04:05")
}

// PlayKey identifies one broadcast: same song + same start time = same play
func (t Track) PlayKey() string {
	return t.Hash + "|" + t.PlayedAtUTC()
}

// Selector defines the data targets (CSS or JSON paths)
type Selector struct {
	Artist   string `json:"artist"`
	Title    string `json:"title"`
	PlayedAt string `json:"played_at"` // optional: start time of the current track
}

// RadioConfig represents a radio configuration loaded from JSON
type RadioConfig struct {
	Name      string   `json:"name"`
	Slug      string   `json:"slug"`
	URL       string   `json:"url"`
	Type      string   `json:"type"`
	Interval  int      `json:"interval_seconds"`
	Country   string   `json:"-"`        // from the parent directory name (ISO 3166-1 alpha-2)
	Timezone  string   `json:"timezone"` // IANA name (e.g. "Europe/Paris"), defaults to the file-level one
	Selectors Selector `json:"selectors"`
}

// GlobalConfig holds app-wide settings
type GlobalConfig struct {
	UserAgent string // sent with every request: stable and honest, so a source can identify us
	Debug     bool   // -debug flag
	Stats     bool   // -stats flag: show what is in the database instead of crawling
	DBPath    string // -db flag or RADIO_SPINLOG_DB: SQLite file or postgres:// URL
	Webhook   string // -webhook flag or RADIO_SPINLOG_WEBHOOK: URL receiving the alerts, empty for none
}
