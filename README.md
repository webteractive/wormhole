# wormhole

[![ci](https://github.com/webteractive/wormhole/actions/workflows/ci.yml/badge.svg)](https://github.com/webteractive/wormhole/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/webteractive/wormhole?sort=semver)](https://github.com/webteractive/wormhole/releases/latest)
[![license](https://img.shields.io/github/license/webteractive/wormhole)](LICENSE)

**Expose a local folder to the public internet through an ephemeral Cloudflare
quick tunnel — drop a file, get a public URL, tear it down.**

`wormhole` serves a local folder over a throwaway `*.trycloudflare.com` URL that
lives only as long as you need it. It's a single, dependency-light Go binary and
needs no Cloudflare account or login.

## Why

Lots of tools and automated workflows produce files locally — often images. Some
consuming services ingest media *only* by fetching a public URL; they have no
upload endpoint. `wormhole` bridges that gap:

> generate a file → `wormhole put file.png` → copy the URL → hand it to the
> service → `wormhole stop`

The public host exists just long enough for the consumer to download the file,
then it's gone.

## Features

- One static binary, standard library only — no runtime to install.
- `serve` (foreground) **and** detached `start` / `stop` / `status` with a pidfile.
- Random subdomain **plus** a random path token, a short TTL, and clean teardown
  (no orphaned processes).
- A readiness probe verifies the public URL actually serves before handing it out.
- Built to be scripting/automation-friendly: `--json` on every command, a clean
  stdout/stderr split, and documented exit codes.

## Requirements

- [`cloudflared`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/)
  on your `PATH`. Account-less quick tunnels need **no** login.
  - macOS: `brew install cloudflared`
  - Linux / Windows: see the download page above.
- Nothing else at runtime. (Building from source needs Go 1.26+.)

## Install

### Prebuilt binary (recommended)

Stable "latest" download URLs (replace `<os>`/`<arch>` with `linux`/`darwin`/`windows`
and `amd64`/`arm64`):

```
https://github.com/webteractive/wormhole/releases/latest/download/wormhole_<os>_<arch>.tar.gz
```

macOS / Linux one-liner:

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case "$arch" in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; esac
curl -fsSL "https://github.com/webteractive/wormhole/releases/latest/download/wormhole_${os}_${arch}.tar.gz" | tar -xz
sudo install "wormhole_${os}_${arch}/wormhole" /usr/local/bin/wormhole
wormhole --version
```

Each release also ships `checksums.txt` (SHA-256) for verification. Windows builds
are `.zip`.

### With Go

```sh
go install github.com/webteractive/wormhole@latest
# note: `go install` builds report version "dev"; released binaries embed the tag.
```

### From source

```sh
git clone https://github.com/webteractive/wormhole
cd wormhole
go build -o wormhole .
```

## Quick start

```sh
# 1. start a tunnel over a fresh temp drop dir (detached; prints the URL once live)
wormhole start --ttl 10m
# drop dir : /tmp/wormhole-1234
# base url : https://random-words.trycloudflare.com/ab12cd.../
# files    : (none yet — drop files into the drop dir)

# 2. publish a file and get its public URL
wormhole put ./image.png
# https://random-words.trycloudflare.com/ab12cd.../image.png

# 3. hand that URL to whatever needs to fetch it...

# 4. tear it all down (stops the static server and cloudflared)
wormhole stop
```

Scripted / automation variant:

```sh
base=$(wormhole start --ttl 10m --json | jq -r .base_url)
url=$(wormhole put ./image.png --json | jq -r .url)
# ... use "$url" ...
wormhole stop
```

You can also point it at an existing folder and serve everything in it:

```sh
wormhole start ./my-images --ttl 30m
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
| `--token S` | random | URL path segment. Must match `^[A-Za-z0-9_-]{1,128}$`. |
| `--types LIST` | any | Comma-separated allowed extensions, e.g. `jpeg,png,webp,gif`. |
| `--max-size SIZE` | none | Reject files larger than e.g. `5MB`. |
| `--ready-timeout D` | `60s` | How long to wait for the tunnel to become reachable. |
| `--no-verify` | off | Skip the readiness probe; publish the URL as soon as it is assigned. |
| `--json` | off | Machine-readable output (available on every subcommand). |

Flags may appear before or after the positional `dir`.

## Output contract (for scripts & automation)

- **stdout carries data only**; all diagnostics, progress, and cloudflared chatter
  go to **stderr**. `wormhole start --json | jq -r .base_url` is safe.
- `--json` emits a single JSON object with a stable schema (`wormhole/v1`):

  ```json
  {
    "schema": "wormhole/v1",
    "base_url": "https://random-words.trycloudflare.com/ab12cd.../",
    "drop_dir": "/tmp/wormhole-1234",
    "port": 51234,
    "token": "ab12cd...",
    "ttl_seconds": 600,
    "expires_at": "2026-01-01T12:34:56Z",
    "pid": 4242,
    "files": [
      { "name": "image.png", "url": ".../image.png", "size": 20481, "content_type": "image/png" }
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
| `7` | File / type / size / name rejected |

## Consumer contract

If the service fetching the URL does so defensively — **redirects disabled**, the
connection **pinned to the resolved IP** (SSRF protection), and accepting only
`image/jpeg`, `image/png`, `image/webp`, `image/gif` (**rejecting SVG**) —
`wormhole` is built to satisfy it:

- URLs serve the file **directly** — no redirect to a CDN.
- The static server sends a correct `Content-Type` from the file (never an active
  document type).
- Keep images reasonably sized; use `--types` / `--max-size` to constrain if desired.

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
  so a file can't execute script on the tunnel origin if opened in a browser.
- The public listener sets read/write/idle timeouts (slowloris-resistant).
- State files (`wormhole.json`, `wormhole.pid`, `wormhole.log`) and the state dir
  are owner-only (`0600` / `0700`); the log records the tokenised URL.
- `put` / `url` reject anything that isn't a plain filename inside the drop dir, and
  `put` won't follow or clobber a symlink at the destination.
- Startup claims the pidfile atomically, so two concurrent starts can't leave an
  orphaned tunnel.

> Account-less quick tunnels have no uptime guarantee, and a freshly provisioned
> hostname can take a few seconds to tens of seconds for DNS to propagate. The
> readiness probe waits for that before reporting success; raise `--ready-timeout`
> on a slow network, or use `--no-verify` to publish immediately and let the
> consumer retry.

## State

A running instance keeps a small footprint under `$XDG_STATE_HOME/wormhole/`
(fallback `~/.wormhole/`): `wormhole.pid`, `wormhole.log` (daemon logs), and
`wormhole.json` (live snapshot used by `status`, `put`, and `url`). All of it is
removed on teardown.

## Development

```sh
go test ./...     # unit tests + a real lifecycle/cleanup test (uses a fake cloudflared)
go vet ./...
gofmt -l .
```

Tests stub `cloudflared` with a fake binary and set `WORMHOLE_SKIP_PROBE=1`, so they
need no network. `WORMHOLE_STATE_DIR` overrides the state directory for isolation.

## Releases

Tagging `vX.Y.Z` triggers the release workflow, which cross-compiles binaries for
linux/macOS/windows on amd64/arm64, attaches archives + `checksums.txt` to a GitHub
release, and embeds the tag as the build version.

```sh
git tag v0.1.0
git push origin v0.1.0
```

## License

[MIT](LICENSE).
