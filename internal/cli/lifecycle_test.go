package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/webteractive/wormhole/internal/proc"
	"github.com/webteractive/wormhole/internal/state"
)

// fakeCloudflared writes a script named "cloudflared" that prints a
// trycloudflare URL to stderr, records its own pid, then becomes a long sleep
// (via exec, so the recorded pid is the process we can later assert is killed).
func fakeCloudflared(t *testing.T, pidFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake cloudflared script needs a POSIX shell")
	}
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo 'INF |  https://fake-lifecycle.trycloudflare.com  |' 1>&2\n" +
		"echo $$ > \"" + pidFile + "\"\n" +
		"exec sleep 120\n"
	path := filepath.Join(binDir, "cloudflared")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func readFakePid(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("fake cloudflared never recorded its pid")
	return 0
}

func TestServeLifecycleStartsAndTearsDown(t *testing.T) {
	t.Setenv("WORMHOLE_STATE_DIR", t.TempDir())
	t.Setenv("WORMHOLE_SKIP_PROBE", "1")
	pidFile := filepath.Join(t.TempDir(), "fake.pid")
	fakeCloudflared(t, pidFile)

	cfg := serveConfig{Dir: t.TempDir(), TTL: 700 * time.Millisecond}

	// runServe blocks until the TTL fires, then tears everything down.
	err := runServe(cfg)
	if err != nil {
		t.Fatalf("runServe returned error: %v", err)
	}

	// cloudflared must be dead — no orphaned process.
	fakePid := readFakePid(t, pidFile)
	if proc.Alive(fakePid) {
		// Give the kill a brief moment in case of scheduling lag.
		time.Sleep(300 * time.Millisecond)
		if proc.Alive(fakePid) {
			t.Errorf("cloudflared (pid %d) still alive after teardown", fakePid)
		}
	}

	// State must be cleaned up.
	if _, err := state.ReadPid(); err == nil {
		t.Error("pidfile not removed after teardown")
	}
	if _, err := state.Read(); err == nil {
		t.Error("state not removed after teardown")
	}
}

func TestServeRefusesWhenAlreadyRunning(t *testing.T) {
	t.Setenv("WORMHOLE_STATE_DIR", t.TempDir())
	t.Setenv("WORMHOLE_SKIP_PROBE", "1")

	// Pretend an instance owned by this very (alive) process is running.
	if err := state.WritePid(os.Getpid()); err != nil {
		t.Fatal(err)
	}

	err := runServe(serveConfig{Dir: t.TempDir(), TTL: time.Second})
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != ExitAlreadyRunning {
		t.Fatalf("expected ExitAlreadyRunning, got %v", err)
	}
}

func TestStopWhenNotRunning(t *testing.T) {
	t.Setenv("WORMHOLE_STATE_DIR", t.TempDir())
	err := runStop(false)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != ExitNotRunning {
		t.Fatalf("expected ExitNotRunning, got %v", err)
	}
}

func TestStatusWhenNotRunning(t *testing.T) {
	t.Setenv("WORMHOLE_STATE_DIR", t.TempDir())
	err := runStatus(true)
	ce, ok := err.(*CodedError)
	if !ok || ce.Code != ExitNotRunning || !ce.Silent {
		t.Fatalf("expected silent ExitNotRunning, got %v", err)
	}
}
