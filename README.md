# radio-spinlog

[![CI](https://github.com/julien-langlois/radio-spinlog/actions/workflows/ci.yml/badge.svg)](https://github.com/julien-langlois/radio-spinlog/actions/workflows/ci.yml)
[![Go version](https://img.shields.io/github/go-mod/go-version/julien-langlois/radio-spinlog)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Downloads](https://img.shields.io/github/downloads/julien-langlois/radio-spinlog/total)](https://github.com/julien-langlois/radio-spinlog/releases)

**Every spin, every station.**

In radio jargon, a *spin* is one airplay of a song. radio-spinlog is a small Go daemon that polls the "now playing" endpoint of radio stations and archives every broadcast into a local SQLite file, or into a PostgreSQL database such as [Supabase](https://supabase.com): one row per play, so the same song appears as often as it is aired.

It is meant as the collection brick of a larger system (play counts per title, per station, per country, per time of day). It only collects; analysis happens downstream, on the database.

## Install

Download a binary for Linux, macOS or Windows from the [releases page](https://github.com/julien-langlois/radio-spinlog/releases): it is a single static file with the station definitions built in.

Or build it with Go 1.26+ (no C compiler needed):

```bash
go install github.com/julien-langlois/radio-spinlog/cmd/radio-spinlog@latest
```

## Quick start

```bash
radio-spinlog -list                          # show the available stations
radio-spinlog                                # track the stations of local.json, or all of them
radio-spinlog -radios fr-nova,fr-nova-soul   # track a specific set
radio-spinlog -stats                         # check what is being archived (latest tracks, totals)
radio-spinlog -check -radios fr-nova         # test a station definition: one request, nothing archived
```

The archive lives in your user data directory (see [Files](#files)), so the same database is used wherever you start the binary from. From a checkout of the repository, replace `radio-spinlog` with `go run ./cmd/radio-spinlog`.

Stop with `Ctrl+C` or `SIGTERM`: workers finish, buffered tracks are written, then the database is closed.

| Flag | Default | Purpose |
|---|---|---|
| `-radios a,b` | — | Comma-separated [slugs](#slugs) to track (`fr-nova,fr-nova-soul`). Overrides the local file. |
| `-list` | — | Print the available stations and exit. |
| `-check` | — | Fetch each selected station once, print what its selectors extract and exit. See [Testing a station](#testing-a-station). |
| `-debug` | off | Log every poll and every HTTP request. |
| `-stats` | — | Print the 10 latest tracks and the totals per station, refreshed every 2 minutes. Reads the database only, so it can run next to a crawling instance. |
| `-db TARGET` | `$RADIO_SPINLOG_DB`, else see [Files](#files) | SQLite database file (its directory is created if needed) or a `postgres://` URL, see [PostgreSQL](#postgresql-supabase). |
| `-configs DIR` | built-in | Directory of `<country>/radios.json` files to use **instead of** the built-in stations. |
| `-local FILE` | see [Files](#files) | File listing the active stations. Optional when left to its default. |
| `-webhook URL` | `$RADIO_SPINLOG_WEBHOOK` | Where to push [alerts](#alerts). None by default. |
| `-heartbeat URL` | `$RADIO_SPINLOG_HEARTBEAT` | URL pinged every 5 minutes while the daemon runs. See [Heartbeat](#heartbeat). |
| `-user-agent` | `radio-spinlog/<version> (personal playlist archive)` | Sent with every request. Add a contact URL or email if you can. |
| `-version` | — | Print the version and exit. |

## Files

Nothing is read from or written to the current directory. Default locations follow each platform's conventions:

| | Database (`archive.db`) | Active stations (`local.json`) |
|---|---|---|
| Linux, BSD | `$XDG_DATA_HOME/radio-spinlog/` or `~/.local/share/radio-spinlog/` | `$XDG_CONFIG_HOME/radio-spinlog/` or `~/.config/radio-spinlog/` |
| macOS | `~/Library/Application Support/radio-spinlog/` | same directory |
| Windows | `%AppData%\radio-spinlog\` | same directory |

`radio-spinlog -h` prints the exact paths on your machine, and the database path is logged at startup. Override them with `-db` (or the `RADIO_SPINLOG_DB` environment variable) and `-local`.

## Configuration

### Stations: `configs/<country>/radios.json`

The stations of this repository's `configs/` directory are compiled into the binary. To track your own, copy that directory, edit it and pass it with `-configs`: every file named `radios.json` under it is loaded, and the built-in ones are no longer used. The parent directory name becomes the station's country, so use ISO 3166-1 alpha-2 codes (`fr`, `us`, `jp`).

```json
{
  "timezone": "Europe/Paris",
  "radios": [
    {
      "name": "Radio Nova",
      "slug": "fr-nova",
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
| `slug` | yes | Identifier stored with every track, of the form `<country>-<station>`. See [Slugs](#slugs). |
| `type` | yes | `"api"` for a JSON endpoint. Any other value means HTML. |
| `url` | yes | Endpoint or page to poll. |
| `interval_seconds` | yes | Polling period, 30 at least. |
| `selectors.artist`, `selectors.title` | yes | Where to read the values (see below). |
| `selectors.played_at` | no | Start time of the current track, when the source exposes it. |
| `timezone` | no | IANA name (`America/New_York`). Set it per file, override it per station. |

### Slugs

A station is identified everywhere by its slug, never by its name: in `-radios`, in `local.json` and in the `radio_slug` column of every archived play. Names are free text and may repeat (there can be a "Radio Nova" in France and another one in Russia); slugs cannot.

- **Form: `<country>-<station>`**, where `<country>` is the name of the directory holding the `radios.json`: `fr-nova` in `configs/fr/`, `ru-nova` in `configs/ru/`. A slug without that prefix, or with the prefix of another country, is skipped with an error.
- **Unique across all countries.** The prefix makes collisions between countries impossible; within one country a duplicate is skipped with an error, and the first definition wins.
- Keep the station part short, lowercase, ASCII, with hyphens (`fr-nova-hip-hop`). This part is a recommendation, it is not checked.
- **A slug is permanent.** It is written on every row of `tracks`, so renaming one splits the history of the station in two. If you must rename, stop the daemon, rename in `radios.json` and `local.json`, then update the archive:

  ```sql
  UPDATE tracks SET radio_slug = 'fr-nova' WHERE radio_slug = 'nova';
  DELETE FROM radios WHERE slug = 'nova';   -- the new slug is recorded at the next start
  ```

### Selectors

Selector syntax depends on `type`:

- **`api`**: [gjson paths](https://github.com/tidwall/gjson/blob/master/SYNTAX.md), for example `now.artist`, or a query such as `#(radio.code=="nova-soul").currentTrack.artist` when the payload is an array of stations. Prefer a query over a positional index: it survives a reordering of the source.
- **HTML**: CSS selectors; the text of the first match is used. Illustrative example: `".now-playing .artist"`.

`played_at` accepts unix seconds or milliseconds, RFC 3339 (`2026-10-05T10:50:07+02:00`), or a local `YYYY-MM-DD HH:MM:SS` which is interpreted in the station's `timezone`. Country is not enough to convert local times (several timezones per country, daylight saving), hence the explicit timezone. Without one, local times are read as UTC and a warning is logged.

Only point `played_at` at a real track start time. A field that changes on every request (server time, for instance) would create a new row at every poll.

### Testing a station

`-check` fetches the selected stations once and shows what would be archived, without opening the database:

```bash
radio-spinlog -check -radios fr-nova
radio-spinlog -configs ./my-configs -check -radios jp-example   # a definition you are writing
```

```text
fr-my-radio  My Radio [fr, Europe/Paris]
  source     api  https://www.myradio.com/spin.json
  artist     ARTIST
  title      TITLE
  played_at  2026-01-01 11:00:00 UTC = 2026-01-01 13:00:00 CEST at the station
  result     OK
```

- The stations are chosen like for a normal run: `-radios`, else `local.json`, else all of them. Stations sharing a URL still cause a single request.
- `result` is `OK` when artist, title and (if a selector is configured) `played_at` were all extracted. Otherwise it says what is missing, and the command exits with status 1.
- `NOTHING EXTRACTED` does not always mean wrong selectors: the station may be in an ad break. Try again a few minutes later before concluding.
- Check that the local time shown for `played_at` matches the station's clock: a wrong `timezone` shows up there.
- It is safe next to a running daemon (no database, no alert), but it is a real request to the source: do not loop on it.

### Active stations: `local.json`

Optional. Create it in your configuration directory (see [Files](#files)) to restrict the tracked stations:

```json
{ "active_radios": ["fr-nova", "fr-nova-hip-hop"] }
```

Selection order: `-radios` flag, then `local.json`, then every configured station. Unknown slugs are ignored with a warning.

### Validation

A station with a missing `slug` or `url`, a slug that is not `<country>-<station>`, an interval under 30 seconds, an unknown timezone or an already used slug is skipped with an error in the logs, as is a `radios.json` that cannot be parsed; the other stations keep running. An invalid `local.json` stops the daemon rather than silently tracking every station.

## PostgreSQL (Supabase)

SQLite is the default and needs no setup. To archive into PostgreSQL instead, give the daemon a connection URL through the `RADIO_SPINLOG_DB` environment variable:

```bash
export RADIO_SPINLOG_DB='postgresql://postgres.<project>:<password>@<region>.pooler.supabase.com:5432/postgres'
radio-spinlog            # crawls into PostgreSQL
radio-spinlog -stats     # reads the same database
```

- **Keep the URL in the environment**, not on the command line: it contains the password, and arguments are visible to every user of the machine (`ps`). `-db` accepts a URL too and wins over the variable; with neither, the SQLite file is used. Logs show the URL with the password masked.
- **Supabase**: copy the *Session pooler* connection string (port 5432) from the dashboard's *Connect* panel. The *Transaction pooler* (port 6543) is not supported, the daemon uses a prepared statement. The *Direct connection* string works as well where IPv6 is available.
- Any PostgreSQL works the same way (self-hosted, Neon, RDS...): nothing is specific to Supabase. Standard URL parameters such as `sslmode=require` are honoured.
- The tables and indexes are created at the first start, in the default schema of the connecting role, which therefore needs the `CREATE` privilege.
- **Row level security is enabled** on both tables without any policy, so that Supabase's auto-generated REST API does not expose them to the anonymous key. The role that created the tables (the one in the URL) is not affected. To read the data with another role, add a policy (`CREATE POLICY ... FOR SELECT`) or give that role `BYPASSRLS`.
- **One collector per station.** Two daemons archiving the same station into the same database would write every play twice. Each daemon therefore reserves its stations at startup: a station already held by another daemon is skipped with an error (`Radio already archived into this database by another collector`), and a daemon left with no station exits. Daemons with different stations can share a database. The reservation is a PostgreSQL advisory lock: it locks no table, so reading the data (dashboard, `-stats`, your own queries) is never blocked. It is released when the daemon stops.
- **The backend is chosen at startup, there is no failover to SQLite.** A failed insert is retried for about a minute (dropped connection, pooler restart); past that the track is lost and logged as `Insert failed, track lost`. If the database is unreachable at startup, the daemon exits.

## Alerts

Problems are always written to the logs. To be told about the ones that need a human, give the daemon a webhook URL; it receives a JSON `POST` for each event:

```bash
export RADIO_SPINLOG_WEBHOOK='https://discord.com/api/webhooks/<id>/<token>'
radio-spinlog
```

| `event` | Sent when |
|---|---|
| `started` | The daemon starts. Also the way to check that the webhook works. |
| `source_stale` | A station has produced no new track for an hour: its source or its selectors may be broken. Sent once per silence, not every hour. |
| `source_recovered` | That station produces tracks again. |
| `track_lost` | A track could not be written to the database after all retries. |
| `writer_failed` | The database writer could not start: nothing is being archived. |

```json
{
  "event": "source_stale",
  "radio": "fr-nova",
  "text": "radio-spinlog: fr-nova has produced no new track for 1h0m0s: its source or its selectors may be broken",
  "content": "radio-spinlog: fr-nova has produced no new track for 1h0m0s: its source or its selectors may be broken"
}
```

- The message is given twice on purpose: Slack-compatible webhooks (Slack, Mattermost) read `text`, Discord reads `content`. Anything else can use `event` and `radio`; `radio` is empty for events that are not about one station.
- **Keep the URL in the environment**: it usually embeds a token. `-webhook` accepts it too. It never appears in the logs.
- The same event for the same station is sent at most once an hour, so a database outage is one `track_lost` alert, not one per track.
- Alerts are best effort: one attempt, 10 seconds timeout, a failure is logged as `Alert not delivered` and never slows the collection down.
- Stations that legitimately air no music for more than an hour (talk shows, night programmes) will raise `source_stale` and `source_recovered` every day.
- A daemon that is not running cannot alert: that case is covered by the [heartbeat](#heartbeat).

### Heartbeat

To be told when the daemon itself stops (crash, machine down, network gone), give it a URL from an uptime monitor such as [healthchecks.io](https://healthchecks.io): it is requested with a `GET` at startup and then every 5 minutes, and the monitor alerts you when the pings stop.

```bash
export RADIO_SPINLOG_HEARTBEAT='https://hc-ping.com/<uuid>'
```

- Configure the monitor with a period of 5 minutes and a grace time of at least 5 more, so that one missed ping is not an incident.
- The ping only says that the process is alive and can reach the Internet. Broken sources and database failures are reported by the [alerts](#alerts).
- Like the webhook, the URL is a secret: keep it in the environment, it never appears in the logs. A failed ping is logged as `Heartbeat not delivered`.

## Running it as a service

The daemon is one static binary with no runtime dependency (the timezone database is built in). Its whole configuration fits in three optional environment variables: `RADIO_SPINLOG_DB`, `RADIO_SPINLOG_WEBHOOK` and `RADIO_SPINLOG_HEARTBEAT`.

### systemd

[`deploy/radio-spinlog.service`](deploy/radio-spinlog.service) runs the daemon as an unprivileged throwaway user, restarts it on failure and keeps the SQLite archive in `/var/lib/radio-spinlog/`:

```bash
sudo install -m 755 radio-spinlog /usr/local/bin/
sudo install -m 644 deploy/radio-spinlog.service /etc/systemd/system/
sudo install -m 600 deploy/radio-spinlog.env.example /etc/radio-spinlog.env   # then edit it
sudo systemctl daemon-reload && sudo systemctl enable --now radio-spinlog
journalctl -u radio-spinlog -f
```

Secrets go in `/etc/radio-spinlog.env` (see [`deploy/radio-spinlog.env.example`](deploy/radio-spinlog.env.example)). To restrict the stations, create `/etc/radio-spinlog/local.json`.

### Docker

```bash
docker build -t radio-spinlog .

# SQLite archive in a named volume
docker run -d --name radio-spinlog --restart unless-stopped -v radio-spinlog-data:/data radio-spinlog

# PostgreSQL: nothing to persist, the variables come from a file kept out of version control
docker run -d --name radio-spinlog --restart unless-stopped --env-file radio-spinlog.env radio-spinlog
```

- The image is about 18 MB, runs as a non-root user and contains only the binary and CA certificates.
- Arguments are passed as usual: `docker run --rm radio-spinlog -check -radios fr-nova`.
- To restrict the stations, use `-radios`; to use your own definitions, mount them and pass `-configs`: `-v ./my-configs:/configs:ro radio-spinlog -configs /configs`.
- `docker stop` sends `SIGTERM`: the daemon finishes cleanly within the default 10 seconds.
- With `--env-file`, write the values without quotes.

## Data

With SQLite everything lives in one file, `archive.db` (see [Files](#files) for its location; WAL mode, safe to read while the daemon runs). PostgreSQL holds the same two tables:

```sql
tracks(id, radio_slug, artist, title, hash, scraped_at, played_at)
radios(slug, name, country, timezone)
```

- **All timestamps are UTC**, in both backends (PostgreSQL columns are `timestamp(0)` without time zone, not `timestamptz`). `scraped_at` is when the track was detected; `played_at` is the start time reported by the source, `NULL` when unknown. `COALESCE(played_at, scraped_at)` gives the best available time.
- `radios.timezone` lets you convert back to the station's local time downstream.
- `hash` is `sha256(lower(trim(artist)) | lower(trim(title)))`: the same song has the same hash on every station.
- `radios` is refreshed from the configuration at each start, for the stations being tracked.

```sql
-- How many times was each title played, and on how many stations?
-- (SQLite; PostgreSQL wants MIN(artist), MIN(title) since they are not in the GROUP BY)
SELECT artist, title, COUNT(*) AS plays, COUNT(DISTINCT radio_slug) AS stations
FROM tracks GROUP BY hash ORDER BY plays DESC LIMIT 20;

-- Plays per country
SELECT r.country, COUNT(*) AS plays
FROM tracks t JOIN radios r ON r.slug = t.radio_slug
GROUP BY r.country;

-- Latest plays of one station
SELECT COALESCE(played_at, scraped_at) AS at_utc, artist, title
FROM tracks WHERE radio_slug = 'fr-nova' ORDER BY id DESC LIMIT 10;
```

## How it works

```text
configs/**/radios.json ─► one goroutine per station ─► channel ─► single database writer
                              │
                              └─ fetch(url): shared per URL
```

- **One worker per station** polls at its interval, after a random start delay of up to 10 s.
- **One request per URL.** Stations configured on the same URL share a single download per interval; each then extracts its own fields.
- **New play detection.** A play is identified by the song hash plus `played_at` when available. A row is written when that key changes. On restart, the last key of each station (if less than 15 minutes old) is reloaded so the song still on air is not inserted twice.
- **Empty artist and title** are treated as an ad break or silence and skipped.
- **Errors** (network failure, non-200 status) trigger an exponential backoff: twice the interval, doubling up to 5 minutes, reset on success.
- **Stale sources.** A station that yields no new track for an hour logs a warning (`No new track for a long time`), repeated hourly until tracks come back. This catches what produces no error: a selector broken by a change at the source, or a frozen endpoint. Stations that legitimately air no music for hours (talk shows) will trigger it too. With a webhook, the first warning and the recovery are pushed as [alerts](#alerts).

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
go test ./...
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run   # lint, same version as CI
```

Linting uses [golangci-lint](https://golangci-lint.run) with its default linters (`go vet` included); the few exclusions are in `.golangci.yml`. The command above needs no installation; with a local `golangci-lint` binary, `golangci-lint run` does the same.

The storage tests always run against SQLite. To run them against PostgreSQL too, point `RADIO_SPINLOG_TEST_POSTGRES` at any server: they work in a temporary schema that is dropped afterwards.

```bash
docker run -d --rm --name spinlog-pg -e POSTGRES_PASSWORD=test -p 127.0.0.1:55432:5432 postgres:17-alpine
RADIO_SPINLOG_TEST_POSTGRES='postgres://postgres:test@127.0.0.1:55432/postgres' go test ./internal/storage/
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
- With SQLite, nothing stops two daemons from archiving the same station into the same file: every play would be written twice. Run one daemon per archive. PostgreSQL is protected, see [PostgreSQL](#postgresql-supabase).
- `-configs` replaces the built-in stations instead of adding to them.

## Disclaimer

Sources are public but mostly undocumented endpoints. This project is intended for personal archiving at a low request rate.

## License

[MIT](LICENSE)
