// Package tunnel launches a Cloudflare quick tunnel via the cloudflared binary
// and extracts the assigned https://<random>.trycloudflare.com URL from its
// log output.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"time"
)

var (
	// ErrNotInstalled means the cloudflared binary was not found on PATH.
	ErrNotInstalled = errors.New("cloudflared not found in PATH")
	// ErrTimeout means cloudflared started but never reported a tunnel URL.
	ErrTimeout = errors.New("timed out waiting for tunnel URL")
)

var reURL = regexp.MustCompile(`https://[a-z0-9][a-z0-9-]*\.trycloudflare\.com`)

// FindURL returns the trycloudflare URL contained in s, if any.
func FindURL(s string) (string, bool) {
	m := reURL.FindString(s)
	return m, m != ""
}

// WaitForURL scans r line by line until it finds a trycloudflare URL or ctx is
// cancelled. It returns ErrTimeout if the stream ends with no match.
func WaitForURL(ctx context.Context, r io.Reader) (string, error) {
	ch := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if u, ok := FindURL(sc.Text()); ok {
				ch <- u
				return
			}
		}
		close(ch)
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case u, ok := <-ch:
		if !ok {
			return "", ErrTimeout
		}
		return u, nil
	}
}

// Tunnel is a running cloudflared quick tunnel.
type Tunnel struct {
	URL string
	cmd *exec.Cmd
}

// Start launches cloudflared against http://127.0.0.1:port and blocks until the
// public URL appears (or timeout). cloudflared logs to stderr.
func Start(ctx context.Context, port int, timeout time.Duration) (*Tunnel, error) {
	path, err := exec.LookPath("cloudflared")
	if err != nil {
		return nil, ErrNotInstalled
	}
	cmd := exec.Command(path, "tunnel", "--url", fmt.Sprintf("http://127.0.0.1:%d", port))
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url, err := WaitForURL(wctx, stderr)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrTimeout
		}
		return nil, err
	}

	// Keep draining stderr so a full pipe never blocks cloudflared.
	go io.Copy(io.Discard, stderr)
	return &Tunnel{URL: url, cmd: cmd}, nil
}

// Stop terminates the cloudflared process.
func (t *Tunnel) Stop() {
	if t == nil || t.cmd == nil || t.cmd.Process == nil {
		return
	}
	_ = t.cmd.Process.Kill()
	_ = t.cmd.Wait()
}

// Pid returns the cloudflared process id (0 if not running).
func (t *Tunnel) Pid() int {
	if t == nil || t.cmd == nil || t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}
