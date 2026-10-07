package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/julien-langlois/radio-spinlog/configs"
	"github.com/julien-langlois/radio-spinlog/internal/domain"
)

// minIntervalSeconds protects the sources (and us from being blacklisted) against a typo in the config
const minIntervalSeconds = 30

const appName = "radio-spinlog"

// dbEnv names the environment variable holding the database target when -db is not given.
// A PostgreSQL URL carries a password: the environment keeps it out of the process list.
const dbEnv = "RADIO_SPINLOG_DB"

// webhookEnv names the environment variable holding the alert webhook when -webhook is not given.
// Webhook URLs embed a token, hence the environment here too.
const webhookEnv = "RADIO_SPINLOG_WEBHOOK"

// defaultDBPath puts the archive in the per-user data directory, so the same database is used
// wherever the binary is started from: $XDG_DATA_HOME or ~/.local/share on Linux and BSD,
// ~/Library/Application Support on macOS, %AppData% on Windows.
func defaultDBPath() string {
	dir, err := os.UserConfigDir() // on macOS and Windows, data and config share this directory
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		// XDG keeps data apart from configuration
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			dir, err = xdg, nil
		} else if home, homeErr := os.UserHomeDir(); homeErr == nil {
			dir, err = filepath.Join(home, ".local", "share"), nil
		}
	}
	if err != nil {
		return filepath.Join("data", "archive.db") // no home directory (minimal container): working directory
	}
	return filepath.Join(dir, appName, "archive.db")
}

// defaultLocalFile is the optional list of active radios, in the per-user configuration directory
// ($XDG_CONFIG_HOME or ~/.config on Linux and BSD, same directories as above elsewhere)
func defaultLocalFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "local.json"
	}
	return filepath.Join(dir, appName, "local.json")
}

// LocalSettings represents the local.json override file
type LocalSettings struct {
	ActiveRadios []string `json:"active_radios"`
}

// Load orchestrates CLI flags, local overrides, and station definitions
func Load(version string) ([]domain.RadioConfig, *domain.GlobalConfig) {
	// 1. Define CLI flags
	cliRadios := flag.String("radios", "", "Comma-separated list of radio slugs to monitor")
	configsDir := flag.String("configs", "", "Directory of <country>/radios.json files, used instead of the built-in stations")
	defaultLocal := defaultLocalFile()
	localFile := flag.String("local", defaultLocal, "JSON file listing the active radios (optional at its default location)")
	dbPath := flag.String("db", "", "SQLite database file or postgres:// URL (default: $"+dbEnv+", else "+defaultDBPath()+")")
	webhook := flag.String("webhook", "", "URL receiving a JSON POST when a source goes quiet or tracks are lost (default: $"+webhookEnv+")")
	userAgent := flag.String("user-agent", "radio-spinlog/"+version+" (personal playlist archive)", "User-Agent sent to the sources; add a contact URL or email if you can")
	debug := flag.Bool("debug", false, "Log every poll and HTTP request")
	stats := flag.Bool("stats", false, "Show the latest archived tracks and totals per radio (refreshed every 2 minutes), without crawling")
	listFlag := flag.Bool("list", false, "List all available radios and exit")
	versionFlag := flag.Bool("version", false, "Print the version and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Println("radio-spinlog", version)
		os.Exit(0)
	}

	globalConfig := &domain.GlobalConfig{UserAgent: *userAgent, Debug: *debug, Stats: *stats, DBPath: cmp.Or(*dbPath, os.Getenv(dbEnv), defaultDBPath()), Webhook: cmp.Or(*webhook, os.Getenv(webhookEnv))}

	// 2. Load station definitions: built into the binary, or from a directory
	var source fs.FS = configs.FS
	if *configsDir != "" {
		source = os.DirFS(*configsDir)
	}
	allRadios := loadRadios(source)

	// 3. Handle list flag
	if *listFlag {
		fmt.Println("Available radios:")
		for _, r := range allRadios {
			fmt.Printf("- %s [%s]\n", r.Slug, r.Country)
		}
		os.Exit(0)
	}

	// 4. Determine active slugs (CLI overrides the local file)
	var activeSlugs []string
	if *cliRadios != "" {
		activeSlugs = strings.Split(*cliRadios, ",")
	} else {
		data, err := os.ReadFile(*localFile)
		switch {
		case err == nil:
			var local LocalSettings
			if err := json.Unmarshal(data, &local); err != nil {
				// Falling back to "all radios" here would poll sources the user did not ask for
				slog.Error("Invalid local file", "file", *localFile, "err", err)
				os.Exit(1)
			}
			activeSlugs = local.ActiveRadios
		case *localFile != defaultLocal || !errors.Is(err, fs.ErrNotExist):
			// The default file is optional; one that was asked for, or that cannot be read, is not
			slog.Error("Cannot read local file", "file", *localFile, "err", err)
			os.Exit(1)
		}
	}

	if len(activeSlugs) == 0 {
		return allRadios, globalConfig
	}
	return filterRadios(allRadios, activeSlugs), globalConfig
}

// filterRadios keeps the requested slugs, in the requested order, warning about unknown ones
func filterRadios(all []domain.RadioConfig, slugs []string) []domain.RadioConfig {
	var filtered []domain.RadioConfig
	for _, slug := range slugs {
		slug = strings.TrimSpace(slug)
		if slug == "" || hasSlug(filtered, slug) {
			continue
		}
		i := slices.IndexFunc(all, func(r domain.RadioConfig) bool { return r.Slug == slug })
		if i < 0 {
			slog.Warn("Unknown radio slug, ignored (use -list to see the available ones)", "slug", slug)
			continue
		}
		filtered = append(filtered, all[i])
	}
	return filtered
}

func hasSlug(radios []domain.RadioConfig, slug string) bool {
	return slices.ContainsFunc(radios, func(r domain.RadioConfig) bool { return r.Slug == slug })
}

// loadRadios reads every *radios.json of fsys. Invalid files and invalid radios
// are logged and skipped, so one bad entry never prevents the others from being tracked.
func loadRadios(fsys fs.FS) []domain.RadioConfig {
	var allRadios []domain.RadioConfig

	err := fs.WalkDir(fsys, ".", func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), "radios.json") {
			return nil
		}

		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			slog.Error("Cannot read config file, skipped", "file", file, "err", err)
			return nil
		}
		var fileData struct {
			Timezone string               `json:"timezone"`
			Radios   []domain.RadioConfig `json:"radios"`
		}
		if err := json.Unmarshal(data, &fileData); err != nil {
			slog.Error("Invalid JSON in config file, skipped", "file", file, "err", err)
			return nil
		}

		country := path.Base(path.Dir(file))
		for _, r := range fileData.Radios {
			r.Country = country
			if r.Timezone == "" {
				r.Timezone = fileData.Timezone
			}
			if r.Slug == "" || r.URL == "" {
				slog.Error("slug and url are required, radio skipped", "radio", r.Slug, "name", r.Name, "file", file)
				continue
			}
			if r.Interval < minIntervalSeconds {
				slog.Error("interval_seconds missing or too low, radio skipped", "radio", r.Slug, "interval", r.Interval, "min", minIntervalSeconds, "file", file)
				continue
			}
			if _, err := time.LoadLocation(r.Timezone); err != nil {
				slog.Error("Unknown timezone, radio skipped", "radio", r.Slug, "timezone", r.Timezone, "file", file)
				continue
			}
			// <country>-<station> keeps slugs unique worldwide: the same station name exists in several countries
			if !strings.HasPrefix(r.Slug, country+"-") || r.Slug == country+"-" {
				slog.Error("slug must be <country>-<station>, with the directory name as country; radio skipped", "radio", r.Slug, "want_prefix", country+"-", "file", file)
				continue
			}
			if hasSlug(allRadios, r.Slug) {
				slog.Error("Duplicate slug, radio skipped (slugs must be unique across all countries)", "radio", r.Slug, "file", file)
				continue
			}
			if r.Selectors.PlayedAt != "" && r.Timezone == "" {
				slog.Warn("played_at selector without timezone: local times will be read as UTC", "radio", r.Slug)
			}
			allRadios = append(allRadios, r)
		}
		return nil
	})
	if err != nil {
		slog.Error("Cannot scan station definitions", "err", err)
	}

	return allRadios
}
