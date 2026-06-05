# wormhole

Expose a local folder to the public internet through an **ephemeral Cloudflare
quick tunnel**, so AI-generated files (mostly images) can be fetched by a public
URL for a short window, then torn down.

It exists for one job: consuming apps (e.g. a Laravel MCP server) ingest media by
fetching a **public image URL** and deliberately have no upload endpoint. `wormhole`
bridges that gap — drop a file in a folder, get a public URL, hand it to the app,
stop.

- Single dependency-light Go binary, no Cloudflare account or login.
- Random subdomain **and** a random path token; short TTL; clean teardown.
- AI-friendly: stdout is pure data, `--json` everywhere, documented exit codes,
  no interactive prompts, and a readiness probe so a handed-out URL actually works.

## Requirements

- [`cloudflared`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/)
  on your `PATH`. Account-less quick tunnels need **no** login.
  - macOS: `brew install cloudflared`

## Install

```sh
# from a clone of this repo
go build -o wormhole .          # produces ./wormhole
# or install into $GOBIN / $GOPATH/bin
go install .
```

## Quick start (the agent workflow)

```sh
# 1. start a tunnel over a fresh temp drop dir (runs detached, prints the URL once live)
wormhole start --ttl 10m
# drop dir : /var/folders/.../wormhole-1234
# base url : https://random-words.trycloudflare.com/ab12cd.../
# files    : (none yet — drop files into the drop dir)

# 2. publish a generated file and get its public URL
wormhole put ./cat.png
# https://random-words.trycloudflare.com/ab12cd.../cat.png

# 3. hand that URL to the consuming app, let it fetch...

# 4. tear it all down (kills the static server and cloudflared)
wormhole stop
```

Scripted / agent variant:

```sh
BASE=$(wormhole start --ttl 10m --json | jq -r .base_url)
URL=$(wormhole put ./cat.png --json | jq -r .url)
# ... use "$URL" ...
wormhole stop
```

## Commands

| Command | Purpose |
| --- | --- |
| `wormhole serve [dir] [flags]` | Foreground: print the URL and serve until Ctrl-C or TTL. |
| `wormhole start [dir] [flags]` | Detached: print the URL once the tunnel is live, then return. |
| `wormhole status` | Show the running instance, or `not running` (exit 6). |
| `wormhole stop` | Stop the instance and tear everything down. |
| `wormhole put <file> [--rename name]` | Copy a file into the drop dir; print its public URL. |
| `wormhole url <filename>` | Print the public URL for a file already in the drop dir. |
| `wormhole --version` / `--help` | Version (+ schema) / help. |

`dir` defaults to a freshly created temp dir, printed so you know where to drop files.

### Flags (`serve` / `start`)

| Flag | Default | Meaning |
| --- | --- | --- |
| `--port N` | `0` | Local listen port (`0` = pick a free port). |
| `--ttl D` | `10m` | Lifetime before auto-teardown (e.g. `30m`, `1h`; `0` = no TTL). |
| `--token S` | random | URL path segment. |
| `--types LIST` | any | Comma-separated allowed extensions, e.g. `jpeg,png,webp,gif`. |
| `--max-size SIZE` | none | Reject files larger than e.g. `5MB`. |
| `--ready-timeout D` | `60s` | How long to wait for the tunnel to become reachable. |
| `--no-verify` | off | Skip the readiness probe; publish the URL as soon as it is assigned. |
| `--json` | off | Machine-readable output (available on every subcommand). |

Flags may appear before or after the positional `dir`.

## Output contract (for scripts and agents)

- **stdout carries data only**; all diagnostics, progress, and cloudflared chatter
  go to **stderr**. `wormhole start --json | jq -r .base_url` is safe.
- `--json` emits a single JSON object with a stable schema (`wormhole/v1`):

  ```json
  {
    "schema": "wormhole/v1",
    "base_url": "https://random-words.trycloudflare.com/ab12cd.../",
    "drop_dir": "/var/folders/.../wormhole-1234",
    "port": 51234,
    "token": "ab12cd...",
    "ttl_seconds": 600,
    "expires_at": "2026-06-05T12:34:56Z",
    "pid": 4242,
    "files": [
      { "name": "cat.png", "url": ".../cat.png", "size": 20481, "content_type": "image/png" }
    ]
  }
  ```

- Errors with `--json` print `{"schema":"wormhole/v1","error":{"code":"...","message":"..."}}`
  to stderr, alongside the matching exit code.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | OK |
| `2` | Usage error |
| `3` | `cloudflared` missing |
| `4` | Tunnel failed / not ready |
| `5` | Already running |
| `6` | Not running |
| `7` | File / type / size rejected |

## Consumer contract

The intended consumer fetches the URL with **redirects disabled**, the connection
**pinned to the resolved IP** (SSRF protection), and accepts only
`image/jpeg`, `image/png`, `image/webp`, `image/gif` (**SVG is rejected**).
`wormhole` is built to satisfy this:

- URLs serve the file **directly** — no redirect to a CDN.
- The static server sends a correct `Content-Type` from the file (never `text/html`).
- Keep images reasonably sized; use `--types`/`--max-size` to constrain if desired.

## Security model

- The `*.trycloudflare.com` subdomain is random and unguessable; files are served
  under an additional **random path token**, so the base URL is not enumerable.
- Only the drop dir is served — **no directory listing, no traversal, no symlink
  escape**.
- A short **TTL** plus explicit `stop` keep the public window small. **That window
  is the security boundary**, not authentication. Use a short `--ttl` and stop when
  done.

Additional hardening:

- Responses always send `X-Content-Type-Options: nosniff`, and active document
  types (`text/html`, SVG, XHTML/XML) are downgraded to `application/octet-stream`
  so a malicious file can't execute script on the tunnel origin if opened in a
  browser.
- The public listener sets read/write/idle timeouts (slowloris-resistant).
- State files (`wormhole.json`, `wormhole.pid`, `wormhole.log`) and the state dir
  are owner-only (`0600`/`0700`) — the log records the tokenised URL, so it is not
  world-readable.
- `put`/`url` reject names that aren't a plain filename inside the drop dir, and
  `put` won't follow or clobber a symlink at the destination.
- A custom `--token` must match `^[A-Za-z0-9_-]{1,128}$`.
- Startup claims the pidfile atomically, so two concurrent starts can't leave an
  orphaned tunnel.

## Notes on quick-tunnel reliability

Account-less quick tunnels have no uptime guarantee, and a freshly provisioned
`*.trycloudflare.com` hostname can take anywhere from a few seconds to tens of
seconds for public DNS to propagate. `wormhole` runs a readiness probe (fetching a
nonce health file through the public URL) before reporting success, so it won't hand
you a dead URL. If your network/region is slow, raise `--ready-timeout`; if you'd
rather publish immediately and let the consumer retry, use `--no-verify`.

## State

A running instance keeps a small footprint under `$XDG_STATE_HOME/wormhole/`
(fallback `~/.wormhole/`): `wormhole.pid`, `wormhole.log` (daemon logs), and
`wormhole.json` (live snapshot used by `status`, `put`, and `url`). All of it is
removed on teardown.

## Development

```sh
go test ./...     # unit tests + a real lifecycle/cleanup test (uses a fake cloudflared)
go vet ./...
```

Tests stub `cloudflared` with a fake binary and set `WORMHOLE_SKIP_PROBE=1`, so they
need no network. `WORMHOLE_STATE_DIR` overrides the state directory for isolation.
