# wormhole — design spec

**Date:** 2026-06-05
**Status:** Approved for implementation

## Summary

`wormhole` is a standalone, dependency-light Go CLI that exposes a local folder to
the public internet through an **ephemeral Cloudflare quick tunnel**
(`*.trycloudflare.com`, no Cloudflare login required). It exists so that
AI-generated files — mostly images — can be fetched by a public URL for a short
window and then torn down.

The motivating consumer is a service (e.g. an MCP server or a media-processing
backend) that ingests media by fetching a **public image URL** and deliberately has
no upload endpoint. `wormhole`
bridges that gap: drop a file in a folder, get a public URL, hand it to the app, stop.

The tool is explicitly **AI-friendly**: machine-readable output, a clean stdout/stderr
split, a stable versioned JSON schema, documented exit codes, no interactive prompts,
and a readiness probe so handed-out URLs always work.

## Goals

- Serve a local "drop dir" over a local static HTTP server (files only, no listing).
- Front it with a `cloudflared` quick tunnel and parse the assigned public URL.
- Print the public base URL and per-file URLs to stdout, with a `--json` mode.
- Provide a foreground blocking mode **and** `start`/`stop`/`status` background
  subcommands with a pidfile.
- Auto-tear down after a configurable TTL (default 10m) and on explicit stop, with
  **no orphaned processes**.
- Make the common agent loop trivial: generate file → get URL → use → stop.

## Non-goals

- No Cloudflare account, named tunnels, or DNS management.
- No persistent hosting; the short TTL is the security boundary, not a feature gap.
- No directory browsing UI; this is a fetch-by-known-URL bridge.

## Decisions (locked)

- **Language:** Go, standard library only. Single cross-platform binary, no runtime for
  users to install. `net/http` gives full control over `Content-Type`, suppresses
  directory listing, and supports the random token path. `os/exec` manages the
  `cloudflared` child cleanly. Tests use `net/http/httptest`.
- **Lifecycle:** both a foreground `serve` mode and detached `start`/`stop`/`status`.
- **File types:** serve **any** file by default (still sending a correct
  `Content-Type`); an optional `--types` allowlist and `--max-size` cap narrow it. The
  consumer's own fetch filter rejects non-image/SVG content regardless.
- **Binary name:** `wormhole`.
- **Daemonization:** `start` re-execs itself detached (`Setpgid`/new session) — the most
  portable approach across macOS/Linux, no double-fork gymnastics.

## Command shape

```
wormhole serve  [dir] [flags]   # foreground: print URLs, block until Ctrl-C / SIGTERM / TTL
wormhole start  [dir] [flags]   # detach, write pidfile, print URLs once tunnel is ready, return
wormhole status                 # read state, print current URLs/state or "not running"
wormhole stop                   # signal the running instance, verify full teardown
wormhole put    <file> [--rename name]   # copy file into the running drop dir, print its URL
wormhole url    <filename>      # compute the URL for a file already in the drop dir
wormhole --help
wormhole --version              # prints tool version + schema version
```

`dir` defaults to a freshly created temp dir (path is printed so you know where to
drop files).

### Flags (serve / start)

- `--port N` — local listen port. Default `0` (OS picks a free port).
- `--ttl DURATION` — lifetime before auto-teardown. Default `10m`. `0` = no TTL.
- `--token STRING` — random path segment. Default: auto-generated, URL-safe, ~16 chars.
- `--types LIST` — comma allowlist, e.g. `jpeg,png,webp,gif`. Default empty = any.
- `--max-size SIZE` — reject files larger than this (e.g. `5MB`). Default `0` = no cap.
- `--json` — machine-readable output on stdout (available on every subcommand).

## Architecture

Single supervisor process (the `wormhole` process). It owns three things and tears all
of them down together:

1. **In-process HTTP file server** — runs in a goroutine on a `net.Listener` bound to
   `127.0.0.1:<port>`. There is no separate server process to leak.
2. **`cloudflared` child** — launched via `os/exec` as
   `cloudflared tunnel --url http://127.0.0.1:<port>`, in its own process group so it
   can be killed as a group.
3. **TTL timer** — fires teardown when the lifetime elapses.

```
agent ──put/start──▶ wormhole supervisor
                       ├─ goroutine: http.Server on 127.0.0.1:PORT  (serves drop dir)
                       ├─ child:     cloudflared ──▶ https://<rand>.trycloudflare.com
                       └─ timer:     TTL ──▶ teardown
public app ──GET https://<rand>.trycloudflare.com/<token>/<file>──▶ tunnel ──▶ 127.0.0.1:PORT
```

### Lifecycle / teardown

- Cleanup is registered via `defer` on every exit path and also triggered by a signal
  handler (`SIGINT`/`SIGTERM`) and by the TTL timer. Teardown:
  1. stop accepting new requests (close listener / `Server.Shutdown`),
  2. kill the cloudflared process group,
  3. remove the pidfile and state file,
  4. exit with the appropriate code.
- `start` re-execs the same binary in detached mode (new process group/session), waits
  until the URL is parsed **and** the readiness probe passes, prints the result, and
  returns while the daemon keeps running. Daemon logs go to a logfile beside the pidfile.
- `stop` reads the pidfile, sends `SIGTERM`, waits briefly, and verifies the process
  (and thus cloudflared) is gone; escalates to `SIGKILL` of the group if needed.
- `start` reaps a **stale pidfile** first: if the recorded PID is dead, the state is
  cleared and startup proceeds; if alive, it errors with exit code `5` (already running).

### Static serving (custom handler, not `http.FileServer`)

- Single route: `GET /<token>/<filename>`. Anything else → `404`.
- The bare `/<token>/` and any directory request → `404` (no listing).
- Requested path is `path.Clean`-ed, joined to the drop dir, then resolved with
  `filepath.EvalSymlinks`; the result must remain inside the drop dir (prefix check) or
  it's rejected — blocks `..` traversal and symlink escape.
- `Content-Type` from `mime.TypeByExtension`, falling back to a content sniff; the
  handler never emits `text/html` for a served asset. `Content-Length` is set.
- **No redirects** — bytes are served directly, satisfying the consumer's
  redirects-disabled fetch.
- `--types` allowlist → non-matching extensions/types return `415`. `--max-size` →
  oversized files return `413`.

### URL parsing (cloudflared)

- Read cloudflared's stderr stream; match `https://[a-z0-9-]+\.trycloudflare\.com`.
- Timeout (~15s) with no match → tear down cleanly and exit code `4`.
- `cloudflared` not found on PATH → exit code `3` with a clear, actionable message.

### Readiness probe

After the URL is parsed, `wormhole` writes a tiny internal health token into the drop
dir under a reserved name and fetches it **through the public URL** until it succeeds
(short bounded retry). Only then is the URL considered live and printed/returned. The
health file is removed after the probe. This guarantees a handed-out URL serves before
an agent passes it to the consuming app.

## AI-friendly contract

- **stdout = data, stderr = diagnostics.** All cloudflared chatter, progress, and TTL
  notices go to stderr. stdout carries only the result, so
  `wormhole start --json | jq -r .base_url` is safe.
- **`--json` everywhere**, stable versioned schema:

  ```json
  {
    "schema": "wormhole/v1",
    "base_url": "https://random-words.trycloudflare.com/<token>/",
    "drop_dir": "/var/folders/.../wormhole-xxxx",
    "port": 51234,
    "token": "abc123...",
    "ttl_seconds": 600,
    "expires_at": "2026-06-05T12:34:56Z",
    "pid": 4242,
    "files": [
      { "name": "cat.png", "url": "https://.../<token>/cat.png", "size": 20481, "content_type": "image/png" }
    ]
  }
  ```

- **Structured errors:** with `--json`, failures emit
  `{ "schema":"wormhole/v1", "error": { "code":"cloudflared_missing", "message":"..." } }`
  on stderr plus the matching exit code — never a bare stack trace.
- **Exit codes:** `0` ok · `2` usage error · `3` cloudflared missing · `4` tunnel
  timeout/failed · `5` already running · `6` not running · `7` file/type/size rejected.
- **No interactive prompts, ever.** No confirmations, no TTY reads. `start` is
  fire-and-forget but only returns after the readiness probe passes.
- **`put` / `url`** read the running instance's token/base_url from the state file, so
  the agent never has to remember them. `put --json` returns just that file's entry.

## State files

Location: `$XDG_STATE_HOME/wormhole/`, falling back to `~/.wormhole/`.

- `wormhole.pid` — supervisor PID.
- `wormhole.log` — daemon log (stderr of the detached process).
- `wormhole.json` — small state blob (`base_url`, `port`, `token`, `drop_dir`,
  `expires_at`) so `status`, `put`, and `url` work without re-parsing cloudflared.

## Security model

- The `trycloudflare` subdomain is random and unguessable; files are additionally
  served under a **random token path segment**, so the base URL is not enumerable.
- Only the drop dir is served — no listing, no traversal, no symlink escape.
- Short default TTL plus explicit `stop` keep the public window small. **The window is
  the security boundary**, not authentication.
- Optional `--types` (image allowlist) and `--max-size` further constrain what is
  exposed. SVG is rejected by the consumer regardless.

## Testing strategy (Go `testing` + `httptest`)

- **URL parser:** extracts/validates the public URL from representative cloudflared log
  samples; handles no-match and timeout paths.
- **Handler:** correct `Content-Type` per extension; directory and bare-token requests
  → 404; traversal (`..`, URL-encoded) and symlink escape blocked; `--types` allowlist
  → 415; `--max-size` → 413; verifies no redirect is emitted.
- **Lifecycle:** pidfile written and removed; stale-pid reaping; cleanup runs on signal
  and TTL. cloudflared is stubbed with a fake binary (a small script that prints a
  trycloudflare-style line) so tests need no network.
- **Output contract:** `--json` shape matches the schema; stdout carries only JSON while
  diagnostics land on stderr; exit codes match the documented table.

## Deliverables

- The `wormhole` CLI with `--help`, the subcommands/flags above, and clean teardown.
- A README: install (`go install` / `go build` / prebuilt binary), the
  drop → run → copy-URL → use → stop workflow, the JSON schema + exit-code table, and
  the security notes.
- Tests covering URL parsing, the static handler, lifecycle/cleanup, and the output
  contract.
