package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/webteractive/wormhole/internal/proc"
	"github.com/webteractive/wormhole/internal/serve"
	"github.com/webteractive/wormhole/internal/state"
	"github.com/webteractive/wormhole/internal/tunnel"
)

// healthName is the reserved file used by the readiness probe.
const healthName = ".wormhole-health"

// serveConfig holds the parsed flags shared by `serve` and `start`.
type serveConfig struct {
	Dir          string
	Port         int
	TTL          time.Duration
	Token        string
	Types        []string
	MaxSize      int64
	ReadyTimeout time.Duration
	NoVerify     bool
	JSON         bool
}

// runServe is the supervisor. It owns the in-process file server, the
// cloudflared child, and the TTL timer, and tears all three down on any exit
// path. It blocks until a signal or the TTL fires.
func runServe(cfg serveConfig) error {
	// Claim the pidfile as an atomic startup lock so two concurrent starts
	// can't both spawn a daemon (and leave one cloudflared orphaned).
	if err := acquireLock(); err != nil {
		return err
	}

	dir, err := resolveDir(cfg.Dir)
	if err != nil {
		state.Remove() // release the lock we just acquired
		return coded(ExitUsage, "bad_dir", "%v", err)
	}
	token := cfg.Token
	if token == "" {
		token = randToken()
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		state.Remove()
		return coded(ExitTunnelFailed, "listen_failed", "cannot listen on 127.0.0.1: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	handler := serve.NewHandler(serve.Options{
		Dir: dir, Token: token, Types: cfg.Types, MaxSize: cfg.MaxSize, Health: healthName,
	})
	// Timeouts matter: this listener is reachable from the public internet via
	// the tunnel, so a slowloris client must not be able to pin connections open.
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go srv.Serve(ln)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var tnRef *tunnel.Tunnel
	teardown := func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		if tnRef != nil {
			tnRef.Stop()
		}
		os.Remove(filepath.Join(dir, healthName))
		state.Remove()
	}
	defer teardown()

	tn, err := tunnel.Start(ctx, port, 20*time.Second)
	if err != nil {
		switch {
		case errors.Is(err, tunnel.ErrNotInstalled):
			return coded(ExitCloudflaredMissing, "cloudflared_missing",
				"cloudflared is not installed or not on PATH; install it from "+
					"https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/")
		case errors.Is(err, tunnel.ErrTimeout):
			return coded(ExitTunnelFailed, "tunnel_timeout", "timed out waiting for the tunnel URL")
		default:
			return coded(ExitTunnelFailed, "tunnel_failed", "failed to start tunnel: %v", err)
		}
	}
	tnRef = tn

	baseURL := tn.URL + "/" + token + "/"

	if !cfg.NoVerify && os.Getenv("WORMHOLE_SKIP_PROBE") == "" {
		if err := probeReady(baseURL, dir, cfg.ReadyTimeout); err != nil {
			return coded(ExitTunnelFailed, "not_ready",
				"tunnel did not become ready within %s: %v (the trycloudflare hostname may still be "+
					"propagating; retry, raise --ready-timeout, or use --no-verify)", cfg.ReadyTimeout, err)
		}
	}

	ttlSeconds := int(cfg.TTL / time.Second)
	expiresAt := ""
	if cfg.TTL > 0 {
		expiresAt = time.Now().Add(cfg.TTL).UTC().Format(time.RFC3339)
	}
	st := state.State{
		Schema: state.Schema, BaseURL: baseURL, DropDir: dir, Port: port,
		Token: token, TTLSeconds: ttlSeconds, ExpiresAt: expiresAt,
		Pid: os.Getpid(), CloudflaredPid: tn.Pid(),
	}
	// The pidfile already exists (acquired as the startup lock); persist the
	// full snapshot used by status/put/url/stop.
	if err := state.Write(st); err != nil {
		return coded(ExitTunnelFailed, "state_error", "cannot write state: %v", err)
	}

	renderSuccess(os.Stdout, cfg.JSON, buildOutput(st))

	var ttlCh <-chan time.Time
	if cfg.TTL > 0 {
		t := time.NewTimer(cfg.TTL)
		defer t.Stop()
		ttlCh = t.C
	}
	select {
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr, "wormhole: signal received, shutting down")
	case <-ttlCh:
		fmt.Fprintln(os.Stderr, "wormhole: ttl reached, shutting down")
	}
	return nil
}

// acquireLock claims the pidfile atomically, refusing if a live instance holds
// it and clearing a stale one left by a crash.
func acquireLock() error {
	for attempt := 0; attempt < 2; attempt++ {
		acquired, existing, err := state.AcquirePid(os.Getpid())
		if err != nil {
			return coded(ExitTunnelFailed, "state_error", "cannot write pidfile: %v", err)
		}
		if acquired {
			return nil
		}
		if proc.Alive(existing) {
			return coded(ExitAlreadyRunning, "already_running",
				"a wormhole instance is already running (pid %d); run 'wormhole stop' first", existing)
		}
		state.Remove() // stale pidfile from a crashed instance — clear and retry
	}
	return coded(ExitAlreadyRunning, "already_running", "could not acquire the instance lock")
}

// resolveDir returns an absolute, existing drop dir, creating a temp dir when
// none is given.
func resolveDir(dir string) (string, error) {
	if dir == "" {
		return os.MkdirTemp("", "wormhole-")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	return abs, nil
}

func randToken() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// probeReady writes a nonce health file and fetches it through the public URL
// (redirects disabled) until it round-trips, proving the tunnel is live before
// the URL is handed out.
func probeReady(baseURL, dir string, timeout time.Duration) error {
	nonce := randToken()
	hp := filepath.Join(dir, healthName)
	if err := os.WriteFile(hp, []byte(nonce), 0o600); err != nil {
		return err
	}
	defer os.Remove(hp)

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	url := baseURL + healthName
	// A freshly provisioned quick-tunnel hostname can take 15-40s for DNS to
	// propagate and edge routing to settle, so keep retrying for a while.
	deadline := time.Now().Add(timeout)
	start := time.Now()
	nextLog := 5 * time.Second
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && string(body) == nonce {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		if elapsed := time.Since(start); elapsed >= nextLog {
			fmt.Fprintf(os.Stderr, "wormhole: waiting for tunnel to become reachable (%ds)\n", int(elapsed.Seconds()))
			nextLog += 5 * time.Second
		}
		time.Sleep(1 * time.Second)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout")
	}
	return lastErr
}
