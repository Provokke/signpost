<div align="center">

# signpost

A URL shortener service in Go with base62 ids, a JSON API and a file-backed store. Standard library only, no database.

![License](https://img.shields.io/badge/LICENSE-MIT-blue?style=for-the-badge)  ![Language](https://img.shields.io/badge/GO-00ADD8?style=for-the-badge&logo=go&logoColor=white)  ![Tests](https://img.shields.io/badge/TESTS-32-brightgreen?style=for-the-badge)

Refer to [LICENSE](LICENSE) for the legal bits.

</div>

## What's in it

- Short ids from a base62 counter (`0-9A-Za-z`). The counter starts at 62 cubed so ids are four characters or more, and ids from deleted or expired links are never handed out again.
- Optional custom aliases (3 to 32 letters, digits, dashes or underscores) and optional expiry with `ttl_seconds`. Expired links answer `410 Gone`.
- Shortening the same URL twice gives you the same id back, unless you asked for an alias or an expiry.
- Only absolute `http` and `https` URLs are accepted, up to 2048 characters.
- A hit counter per link. `HEAD` requests do not count.
- A store that is a single append-only JSON lines file. It is replayed on start, a half-written last line from a crash is trimmed off, and a corrupt line anywhere else is reported rather than skipped. `Compact` rewrites it without deleted and expired links, and the executable does that hourly by default.

```
curl -s -X POST localhost:8080/api/links -d '{"url": "https://example.com/a/very/long/path", "ttl_seconds": 3600}'
```

## 🏗️ Architecture

```
 HTTP ──► Server (net/http ServeMux, Go 1.22 patterns)
            POST   /api/links         create
            GET    /api/links         list, newest first
            GET    /api/links/{id}    details and hit count
            DELETE /api/links/{id}    remove
            GET    /health
            GET    /{id}              302 redirect
              │
              ▼
          Store ── map[id]Link + map[url]id ── append-only log file
```

- **internal/shortener/base62.go** is `Encode` and `Decode`, with overflow checking on decode.
- **internal/shortener/store.go** is the store. Creates and deletes are fsynced. Hits are appended without a sync, because losing the last few counts in a crash is a fair trade for not syncing on every redirect.
- **internal/shortener/server.go** is the HTTP layer. Request bodies are capped at 1 MiB, unknown JSON fields are rejected, and errors come back as `{"error": "..."}` with a sensible status (400, 404, 409, 410).
- **cmd/signpost** is the executable, with periodic compaction and graceful shutdown.

## 🚀 Quick start

Go 1.24 or newer.

```
go build -o signpost ./cmd/signpost
./signpost -addr :8080 -data signpost.log -base-url http://localhost:8080
```

Flags: `-addr`, `-data` (store file), `-base-url` (used when building `short_url`) and `-compact` (interval, `0` to turn off).

```
curl -s -X POST localhost:8080/api/links -d '{"url": "https://example.com/docs", "alias": "docs"}'
curl -i localhost:8080/docs
curl -s localhost:8080/api/links/docs
```

The first call answers `201` with `id`, `short_url`, `url`, `created` and `hits`. The second answers `302` with `Location: https://example.com/docs`.

## 🧪 Testing

```
go test -race -count=1 ./...
```

32 tests, all passing under the race detector. The HTTP tests go through the real handler with `httptest` and a fake clock:

- Base62: known values, round trips up to the largest uint64, bad input, ordering (4).
- Store: sequential ids, deduplication, aliases, URL validation, ttl and expiry, alias reuse after expiry, deletes, reopening from disk, a truncated tail, a corrupt middle, compaction, listing and paging, concurrent creates (16).
- API: create and redirect, aliases and ttl, validation errors, conflicts, hit counting, `HEAD`, 404s, delete, list, health, method checks (12).
