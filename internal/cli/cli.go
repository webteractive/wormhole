// Package cli implements wormhole's command dispatch, flag parsing, and the
// machine-readable output contract.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/webteractive/wormhole/internal/state"
)

// Version is the tool version reported by `wormhole --version`. It defaults to
// "dev" for source/`go install` builds and is overridden at release time via
// -ldflags "-X .../internal/cli.Version=<tag>".
var Version = "dev"

// A custom --token must stay URL-safe and single-segment; it is the secret that
// makes the public path unguessable.
const tokenPattern = `^[A-Za-z0-9_-]{1,128}$`

var validToken = regexp.MustCompile(tokenPattern)

// Main is the entry point; it returns the process exit code.
func Main() int {
	args := os.Args[1:]
	if len(args) == 0 {
		usage(os.Stderr)
		return ExitUsage
	}

	switch args[0] {
	case "-h", "--help", "help":
		usage(os.Stdout)
		return ExitOK
	case "-V", "--version", "version":
		fmt.Printf("wormhole %s (schema %s)\n", Version, state.Schema)
		return ExitOK
	}

	cmd, rest := args[0], args[1:]
	jsonMode := hasJSON(rest)

	var err error
	switch cmd {
	case "serve":
		var cfg serveConfig
		if err = parseServeFlags("serve", rest, &cfg); err == nil {
			err = runServe(cfg)
		}
	case "start":
		var cfg serveConfig
		// Validate flags up front, but hand the original args to the child so
		// it parses them itself.
		if err = parseServeFlags("start", rest, &cfg); err == nil {
			err = runStart(rest, cfg)
		}
	case "stop":
		err = runStop(jsonMode)
	case "status":
		err = runStatus(jsonMode)
	case "put":
		err = runPutCmd(rest)
	case "url":
		err = runURLCmd(rest)
	default:
		fmt.Fprintf(os.Stderr, "wormhole: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return ExitUsage
	}

	if err != nil {
		ce, ok := err.(*CodedError)
		if !ok {
			ce = coded(ExitTunnelFailed, "internal_error", "%v", err)
		}
		if !ce.Silent {
			renderError(os.Stderr, jsonMode, ce)
		}
		return ce.Code
	}
	return ExitOK
}

func runPutCmd(args []string) error {
	fs := flag.NewFlagSet("put", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rename := fs.String("rename", "", "store the file under this name")
	jsonMode := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(reorder(args, map[string]bool{"json": true})); err != nil {
		return coded(ExitUsage, "usage", "%v", err)
	}
	if fs.NArg() < 1 {
		return coded(ExitUsage, "usage", "usage: wormhole put <file> [--rename name] [--json]")
	}
	return runPut(fs.Arg(0), *rename, *jsonMode)
}

func runURLCmd(args []string) error {
	fs := flag.NewFlagSet("url", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonMode := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(reorder(args, map[string]bool{"json": true})); err != nil {
		return coded(ExitUsage, "usage", "%v", err)
	}
	if fs.NArg() < 1 {
		return coded(ExitUsage, "usage", "usage: wormhole url <filename> [--json]")
	}
	return runURL(fs.Arg(0), *jsonMode)
}

func parseServeFlags(name string, args []string, cfg *serveConfig) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	port := fs.Int("port", 0, "local listen port (0 = pick a free port)")
	ttl := fs.Duration("ttl", 10*time.Minute, "lifetime before auto-teardown (0 = none)")
	token := fs.String("token", "", "URL path segment (default: random)")
	types := fs.String("types", "", "comma-separated allowed extensions (default: any)")
	maxSize := fs.String("max-size", "", "max served file size, e.g. 5MB (default: none)")
	readyTimeout := fs.Duration("ready-timeout", 60*time.Second, "how long to wait for the tunnel to become reachable")
	noVerify := fs.Bool("no-verify", false, "skip the readiness probe; publish the URL as soon as cloudflared reports it")
	jsonMode := fs.Bool("json", false, "machine-readable output")

	if err := fs.Parse(reorder(args, map[string]bool{"json": true, "no-verify": true})); err != nil {
		return coded(ExitUsage, "usage", "%v", err)
	}
	if *ttl < 0 {
		return coded(ExitUsage, "usage", "--ttl must not be negative")
	}
	if *token != "" && !validToken.MatchString(*token) {
		return coded(ExitUsage, "usage",
			"--token must match %s (it is the URL path secret)", tokenPattern)
	}
	cfg.Port = *port
	cfg.TTL = *ttl
	cfg.Token = *token
	cfg.ReadyTimeout = *readyTimeout
	cfg.NoVerify = *noVerify
	cfg.JSON = *jsonMode
	for _, t := range strings.Split(*types, ",") {
		if t = strings.TrimSpace(t); t != "" {
			cfg.Types = append(cfg.Types, t)
		}
	}
	if *maxSize != "" {
		n, err := parseSize(*maxSize)
		if err != nil {
			return coded(ExitUsage, "usage", "invalid --max-size: %v", err)
		}
		cfg.MaxSize = n
	}
	if fs.NArg() > 0 {
		cfg.Dir = fs.Arg(0)
	}
	return nil
}

// reorder moves flags ahead of positional arguments so the standard flag
// package (which stops at the first positional) accepts them in any order —
// friendlier for both humans and agents. boolFlags names flags that take no
// value.
func reorder(args []string, boolFlags map[string]bool) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(a, "=") || boolFlags[name] {
			continue
		}
		// Consume the following token as this flag's value.
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, pos...)
}

func hasJSON(args []string) bool {
	for _, a := range args {
		if a == "--json" || a == "-json" {
			return true
		}
	}
	return false
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "KB"):
		mult, s = 1<<10, strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "MB"):
		mult, s = 1<<20, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "GB"):
		mult, s = 1<<30, strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("must be non-negative")
	}
	return int64(n * float64(mult)), nil
}

func usage(w io.Writer) {
	fmt.Fprint(w, `wormhole — expose a local folder via an ephemeral Cloudflare quick tunnel

USAGE
  wormhole serve  [dir] [flags]    Run in the foreground; serve until Ctrl-C or TTL
  wormhole start  [dir] [flags]    Run detached; print the URL once the tunnel is live
  wormhole status                  Show the running instance (or "not running")
  wormhole stop                    Stop the running instance and tear everything down
  wormhole put    <file> [--rename name]   Copy a file into the drop dir; print its URL
  wormhole url    <filename>       Print the public URL for a file in the drop dir
  wormhole --version               Print version and schema version
  wormhole --help                  Show this help

FLAGS (serve / start)
  --port N        Local listen port (0 = pick a free port)
  --ttl D         Lifetime before auto-teardown, e.g. 10m, 1h, 0 for none (default 10m)
  --token S       URL path segment (default: random, unguessable)
  --types LIST    Comma-separated allowed extensions, e.g. jpeg,png,webp,gif (default: any)
  --max-size SIZE Reject files larger than SIZE, e.g. 5MB (default: none)
  --ready-timeout D  Wait this long for the tunnel to become reachable (default 60s)
  --no-verify     Skip the readiness probe; publish the URL as soon as it is assigned
  --json          Machine-readable output (every subcommand)

EXIT CODES
  0 ok   2 usage   3 cloudflared missing   4 tunnel failed
  5 already running   6 not running   7 file/type/size rejected

OUTPUT
  stdout carries data only; diagnostics go to stderr. With --json, stdout emits a
  single JSON object (schema "wormhole/v1") and errors emit {"error":{...}} on stderr.

Requires cloudflared on PATH. Quick tunnels need no Cloudflare login.
`)
}
