# radio-spinlog

[![CI](https://github.com/julien-langlois/radio-spinlog/actions/workflows/ci.yml/badge.svg)](https://github.com/julien-langlois/radio-spinlog/actions/workflows/ci.yml)
[![Go version](https://img.shields.io/github/go-mod/go-version/julien-langlois/radio-spinlog)](go.mod)
[![Go Report Card](https://goreportcard.com/badge/github.com/julien-langlois/radio-spinlog)](https://goreportcard.com/report/github.com/julien-langlois/radio-spinlog)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Downloads](https://img.shields.io/github/downloads/julien-langlois/radio-spinlog/total)](https://github.com/julien-langlois/radio-spinlog/releases)

**Every spin, every station.**

In radio jargon, a *spin* is one airplay of a song. radio-spinlog is a small Go daemon that polls the "now playing" endpoint of radio stations and archives every broadcast into SQLite: one row per play, so the same song appears as often as it is aired.

It is meant as the collection brick of a larger system (play counts per title, per station, per country, per time of day). It only collects; analysis happens downstream, on the database.

## Install

Download a binary for Linux, macOS or Windows from the [releases page](https://github.com/julien-langlois/radio-spinlog/releases): it is a single static file with the station definitions built in.

Or build it with Go 1.26+ (no C compiler needed):

```bash
go install github.com/julien-langlois/radio-spinlog/cmd/radio-spinlog@latest
```

## Quick start

```bash
radio-spinlog -list                    # show the available stations
radio-spinlog                          # track the stations of local.json, or all of them
radio-spinlog -radios nova,nova-soul   # track a specific set
radio-spinlog -stats                   # check what is being archived (latest tracks, totals)
```

The archive lives in your user data directory (see [Files](#files)), so the same database is used wherever you start the binary from. From a checkout of the repository, replace `radio-spinlog` with `go run ./cmd/radio-spinlog`.

Stop with `Ctrl+C` or `SIGTERM`: workers finish, buffered tracks are written, then the database is closed.

| Flag | Default | Purpose |
|---|---|---|
| `-radios a,b` | — | Comma-separated slugs to track. Overrides the local file. |
| `-list` | — | Print the available stations and exit. |
| `-debug` | off | Log every poll and every HTTP request. |
| `-stats` | — | Print the 10 latest tracks and the totals per station, refreshed every 2 minutes. Reads the database only, so it can run next to a crawling instance. |
| `-db FILE` | see [Files](#files) | SQLite database file; its directory is created if needed. |
| `-configs DIR` | built-in | Directory of `<country>/radios.json` files to use **instead of** the built-in stations. |
| `-local FILE` | see [Files](#files) | File listing the active stations. Optional when left to its default. |
| `-user-agent` | `radio-spinlog/<version> (personal playlist archive)` | Sent with every request. Add a contact URL or email if you can. |
| `-version` | — | Print the version and exit. |

## Files

Nothing is read from or written to the current directory. Default locations follow each platform's conventions:

| | Database (`archive.db`) | Active stations (`local.json`) |
|---|---|---|
| Linux, BSD | `$XDG_DATA_HOME/radio-spinlog/` or `~/.local/share/radio-spinlog/` | `$XDG_CONFIG_HOME/radio-spinlog/` or `~/.config/radio-spinlog/` |
| macOS | `~/Library/Application Support/radio-spinlog/` | same directory |
| Windows | `%AppData%\radio-spinlog\` | same directory |

`radio-spinlog -h` prints the exact paths on your machine, and the database path is logged at startup. Override them with `-db` and `-local`.

## Configuration

### Stations: `configs/<country>/radios.json`

The stations of this repository's `configs/` directory are compiled into the binary. To track your own, copy that directory, edit it and pass it with `-configs`: every file named `radios.json` under it is loaded, and the built-in ones are no longer used. The parent directory name becomes the station's country, so use ISO 3166-1 alpha-2 codes (`fr`, `us`, `jp`).

```json
{
  "timezone": "Europe/Paris",
  "radios": [
    {
      "name": "Radio Nova",
      "slug": "nova",
      "type": "api",
      "url": "https://www.nova.fr/radios-data/www.nova.fr/all.json",
      "interval_seconds": 120,
      "selectors": {
        "artist": "#(radio.code==\"radio-nova\").currentTrack.artist",
        "title": "#(radio.code==\"radio-nova\").currentTrack.title",
        "played_at": "#(radio.code==\"radio-nova\").currentTrack.diffusion_date"
      }
    }
  ]
}
```

| Field | Required | Description |
|---|---|---|
| `name` | yes | Display name. |
| `slug` | yes | Identifier stored with every track. **Must be unique across all countries**; a duplicate is skipped with an error. |
| `type` | yes | `"api"` for a JSON endpoint. Any other value means HTML. |
| `url` | yes | Endpoint or page to poll. |
| `interval_seconds` | yes | Polling period, 30 at least. |
| `selectors.artist`, `selectors.title` | yes | Where to read the values (see below). |
| `selectors.played_at` | no | Start time of the current track, when the source exposes it. |
| `timezone` | no | IANA name (`America/New_York`). Set it per file, override it per station. |

Selector syntax depends on `type`:

- **`api`**: [gjson paths](https://github.com/tidwall/gjson/blob/master/SYNTAX.md), for example `now.artist`, or a query such as `#(radio.code=="nova-soul").currentTrack.artist` when the payload is an array of stations. Prefer a query over a positional index: it survives a reordering of the source.
- **HTML**: CSS selectors; the text of the first match is used. Illustrative example: `".now-playing .artist"`.

`played_at` accepts unix seconds or milliseconds, RFC 3339 (`2026-10-05T10:50:07+02:00`), or a local `YYYY-MM-DD HH:MM:SS` which is interpreted in the station's `timezone`. Country is not enough to convert local times (several timezones per country, daylight saving), hence the explicit timezone. Without one, local times are read as UTC and a warning is logged.

Only point `played_at` at a real track start time. A field that changes on every request (server time, for instance) would create a new row at every poll.

### Active stations: `local.json`

Optional. Create it in your configuration directory (see [Files](#files)) to restrict the tracked stations:

```json
{ "active_radios": ["nova", "nova-hip-hop"] }
```

Selection order: `-radios` flag, then `local.json`, then every configured station. Unknown slugs are ignored with a warning.

### Validation

A station with a missing `slug` or `url`, an interval under 30 seconds, an unknown timezone or an already used slug is skipped with an error in the logs, as is a `radios.json` that cannot be parsed; the other stations keep running. An invalid `local.json` stops the daemon rather than silently tracking every station.

## Data

Everything lives in one SQLite file, `archive.db` (see [Files](#files) for its location; WAL mode, safe to read while the daemon runs).

```sql
tracks(id, radio_slug, artist, title, hash, scraped_at, played_at)
radios(slug, name, country, timezone)
```

- **All timestamps are UTC.** `scraped_at` is when the track was detected; `played_at` is the start time reported by the source, `NULL` when unknown. `COALESCE(played_at, scraped_at)` gives the best available time.
- `radios.timezone` lets you convert back to the station's local time downstream.
- `hash` is `sha256(lower(trim(artist)) | lower(trim(title)))`: the same song has the same hash on every station.
- `radios` is refreshed from the configuration at each start, for the stations being tracked.

```sql
-- How many times was each title played, and on how many stations?
SELECT artist, title, COUNT(*) AS plays, COUNT(DISTINCT radio_slug) AS stations
FROM tracks GROUP BY hash ORDER BY plays DESC LIMIT 20;

-- Plays per country
SELECT r.country, COUNT(*) AS plays
FROM tracks t JOIN radios r ON r.slug = t.radio_slug
GROUP BY r.country;

-- Latest plays of one station
SELECT COALESCE(played_at, scraped_at) AS at_utc, artist, title
FROM tracks WHERE radio_slug = 'nova' ORDER BY id DESC LIMIT 10;
```

## How it works

```text
configs/**/radios.json ─► one goroutine per station ─► channel ─► single SQLite writer
                              │
                              └─ fetch(url): shared per URL
```

- **One worker per station** polls at its interval, after a random start delay of up to 10 s.
- **One request per URL.** Stations configured on the same URL share a single download per interval; each then extracts its own fields.
- **New play detection.** A play is identified by the song hash plus `played_at` when available. A row is written when that key changes. On restart, the last key of each station (if less than 15 minutes old) is reloaded so the song still on air is not inserted twice.
- **Empty artist and title** are treated as an ad break or silence and skipped.
- **Errors** (network failure, non-200 status) trigger an exponential backoff: twice the interval, doubling up to 5 minutes, reset on success.
- **Stale sources.** A station that yields no new track for an hour logs a warning (`No new track for a long time`), repeated hourly until tracks come back. This catches what produces no error: a selector broken by a change at the source, or a frozen endpoint. Stations that legitimately air no music for hours (talk shows) will trigger it too.

### Being a polite client

The daemon is designed not to get blacklisted:

- conditional requests (`If-None-Match` / `If-Modified-Since`): an unchanged source answers `304` with no payload;
- `Retry-After` is obeyed on error responses: no request goes to that URL before the delay expires;
- a fixed, honest User-Agent instead of browser impersonation;
- a 30-second floor on `interval_seconds`, a 15-second request timeout, a 10 MiB response cap.

Keep intervals reasonable (the bundled configuration uses 120 s) and check the terms of use of the sources you add.

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

Logs are JSON on stdout. With `-debug`, every network request is logged as `HTTP request` with its URL and status.

### Releasing

Pushing a version tag builds and publishes the binaries with [GoReleaser](https://goreleaser.com) (`.goreleaser.yaml`, `.github/workflows/release.yml`):

```bash
git tag v0.1.0
git push origin v0.1.0
```

A published version is permanent: the Go module proxy keeps it, so fix mistakes with a new tag rather than by moving one.

## Known limitations

- Without a `played_at` selector, the same song played twice in a row (or on both sides of an ad break) is counted once.
- A track shorter than the polling interval can be missed entirely.
- The HTML scraper has not been exercised against a real station yet; pages rendered by JavaScript will not work.
- Alerts are log lines only: nothing pushes a notification when a source breaks.
- `-configs` replaces the built-in stations instead of adding to them.

## Disclaimer

Sources are public but mostly undocumented endpoints. This project is intended for personal archiving at a low request rate.

## License

[MIT](LICENSE)
