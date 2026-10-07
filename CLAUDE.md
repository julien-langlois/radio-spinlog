# radio-spinlog

Go daemon that polls radio "now playing" endpoints and archives every broadcast into SQLite (default) or PostgreSQL (Supabase and others). It is the collection brick of a larger analytics system (play counts per title / station / country). See `README.md` for the configuration format and the schema.

## Commands

```bash
go build ./...
go vet ./...
go test ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run   # same version as CI, config in .golangci.yml
go run ./cmd/radio-spinlog [-radios a,b] [-list] [-debug] [-stats] [-db FILE|postgres://URL] [-webhook URL] [-configs DIR] [-local FILE] [-user-agent "..."] [-version]
```

- Stations are embedded from `configs/` at build time (`-configs DIR` reads a directory instead). `-db` and `-local` default to per-user directories (`~/.local/share/radio-spinlog/archive.db` and `~/.config/radio-spinlog/local.json` on Linux; see `defaultDBPath` / `defaultLocalFile`), never the working directory.
- The database target is `-db`, else the `RADIO_SPINLOG_DB` environment variable, else the SQLite default. A `postgres://` or `postgresql://` URL selects PostgreSQL.
- Alerts go to `-webhook`, else the `RADIO_SPINLOG_WEBHOOK` environment variable; none by default.
- `RADIO_SPINLOG_TEST_POSTGRES=postgres://...` also runs the storage tests against PostgreSQL (in a temporary schema); CI sets it with a service container.
- No cgo: both drivers are pure Go (`modernc.org/sqlite`, `github.com/jackc/pgx/v5/stdlib`). Keep it that way, releases are cross-compiled with `CGO_ENABLED=0`.
- Releases: pushing a `v*` tag runs GoReleaser (`.goreleaser.yaml`, `.github/workflows/release.yml`). Never tag or push without being asked; a published version cannot be changed.
- A real run hits live third-party endpoints and writes to the user's real archive unless `-db` points elsewhere. Keep manual runs short and do not lower intervals to test faster.

## Layout

```
cmd/radio-spinlog/main.go         wiring: config → database → one worker per radio → graceful shutdown
internal/config/loader.go         CLI flags, station loading (embedded or -configs), validation, local.json filter
internal/domain/models.go         Track (PlayKey), RadioConfig, Selector, GlobalConfig
internal/crawler/worker.go        per-radio loop: fetch, backoff, new-play detection, stale-source warning
internal/crawler/fetch.go         shared HTTP layer: per-URL cache, conditional requests, Retry-After
internal/crawler/scraper.go       Scraper interface + StaticScraper (HTML, goquery)
internal/crawler/api_scraper.go   ApiScraper (JSON, gjson)
internal/crawler/playedat.go      source timestamp → UTC
internal/crawler/hash.go          song fingerprint
internal/alert/alert.go           webhook alerts (stale source, lost tracks): async, per-event cooldown, no-op without a URL
internal/storage/db.go            SQLite and PostgreSQL backends: schemas, radios upsert, single writer with insert retry, LastKey
internal/storage/stats.go         -stats view (sanity check only; analytics belong to a separate tool)
configs/<country>/radios.json     station definitions; directory name = country code
configs/embed.go                  embeds the station definitions into the binary
```

## Invariants

These are deliberate. Do not change them without being asked.

- **`tracks` is a play history, not a catalog.** One row per broadcast. Never add a uniqueness constraint or `ON CONFLICT ... DO NOTHING` on `hash`; replays must be kept.
- **All timestamps in the database are UTC**, formatted `YYYY-MM-DD HH:MM:SS` (same as SQLite's `CURRENT_TIMESTAMP`). Local time is derived downstream from `radios.timezone`. Timezones are IANA names per station, never inferred from the country.
- **A play is identified by `Track.PlayKey()`** (hash + `played_at` when known). The worker's debouncing and `DB.LastKey` must stay consistent with it, on both backends (`DB.timeText`).
- **Only `DB.StartWriter` writes tracks.** Workers send on the channel.
- **One backend per run, no failover.** The target picks SQLite or PostgreSQL at startup; SQLite never takes over when PostgreSQL is down (the writer retries, then logs the lost track).
- **Both backends behave the same.** Queries are written once with `?` placeholders (`DB.q`); anything dialect-specific goes behind `DB.pg` and is covered by `TestReplaysAreKept` on both. Nothing Supabase-specific: plain PostgreSQL only.
- **A database URL is a secret.** Log it only through `storage.Redact`, never print it in `-h` output or error messages.
- **The webhook URL is a secret too** (it embeds a token) and is never logged, including through `*url.Error` (see `alert.post`).
- **Alerts never get in the way of collection.** `alert.Send` is asynchronous, best effort and muted by a cooldown; every alert is also a log line at the call site. No retries, no queue.
- **All HTTP toward sources goes through `crawler.fetch`.** It guarantees one request per URL per interval regardless of how many radios share that URL, sends conditional requests and obeys `Retry-After`. Never call `http.Get` or build another client in a scraper. The only other HTTP client is the webhook one in `internal/alert`.
- **Never make the daemon more aggressive toward sources**: no User-Agent spoofing or rotation, no interval under `minIntervalSeconds`, no retry loops outside the worker's backoff. Avoiding blacklisting is a hard requirement.
- **Nothing source-specific in Go code.** Nova is only the first example; the target is any station worldwide. A new source must be expressible in `radios.json` (selectors, timezone). If it is not, extend the generic mechanism.
- **Invalid configuration is logged and skipped, never silently accepted** (see `loadRadios`). New config fields that can harm data or sources need the same validation.
- **Slugs are `<country>-<station>` and globally unique** (primary key of `radios`): the prefix is the name of the directory holding the `radios.json` (`fr-nova` in `configs/fr/`). A wrong prefix or a duplicate is rejected at load. Station names may repeat across countries; slugs never do.
- **A slug is permanent.** It is stored on every row of `tracks`; renaming one splits the station's history. Never rename a slug in `configs/` without being asked, and document the `UPDATE` when it happens (README, "Slugs").
- **Shutdown order** in `main.go`: wait for workers, close the channel, wait for the writer, close the database.

## Conventions

- English only for identifiers, log messages, comments and documentation.
- Logging with `log/slog`, key/value pairs, radio slug under the `"radio"` key.
- Adding a station is a configuration change, not a code change: `configs/<country>/radios.json`, slug `<country>-<station>`.
- Schema changes must keep existing databases working, on both backends: `CREATE ... IF NOT EXISTS` plus an idempotent `ALTER` in `openSQLite` (see `played_at`) and `ADD COLUMN IF NOT EXISTS` in `postgresSchema`.
- Tests are few and targeted (`loader_test.go`, `worker_test.go`, `fetch_test.go`, `playedat_test.go`, `db_test.go`, `alert_test.go`). Add one when touching deduplication, time parsing, fetching or storage; use `httptest`, never a live source.
- `ponytail:` comments mark deliberate simplifications with their known ceiling.

## Known gaps

- `flag.Parse` and `os.Exit` live inside `config.Load`; test `loadRadios` and `filterRadios` instead.
- Alerts (`internal/alert`) cover stale sources and lost tracks, not a daemon that is down. The former `internal/notifier` (WhatsApp, i18n) was removed as dead code; an archive sits in `data/notifier-backup.tar.gz`.
- `StaticScraper` has never run against a real station.
- `data/` and `local.json` at the repository root are leftovers from before the per-user directories; they are git-ignored and no longer read by default.
