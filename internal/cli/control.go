package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/webteractive/wormhole/internal/proc"
	"github.com/webteractive/wormhole/internal/state"
)

// runStart launches the supervisor detached, then waits until it has published
// a ready state (or failed) before returning the URL. It never leaves the
// daemon running on failure.
func runStart(childArgs []string, cfg serveConfig) error {
	// Fast-fail if an instance is already live. The authoritative lock is the
	// pidfile the daemon claims atomically (acquireLock); we don't clear state
	// here so we can't clobber a healthy instance.
	if pid, err := state.ReadPid(); err == nil && proc.Alive(pid) {
		return coded(ExitAlreadyRunning, "already_running",
			"a wormhole instance is already running (pid %d); run 'wormhole stop' first", pid)
	}

	exe, err := os.Executable()
	if err != nil {
		return coded(ExitTunnelFailed, "exec_error", "cannot locate own binary: %v", err)
	}
	logf, err := state.LogFile()
	if err != nil {
		return coded(ExitTunnelFailed, "state_error", "cannot open log: %v", err)
	}
	defer logf.Close()

	cmd := exec.Command(exe, append([]string{"serve"}, childArgs...)...)
	cmd.Env = append(os.Environ(), "WORMHOLE_DAEMON=1")
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	proc.SetSession(cmd)
	if err := cmd.Start(); err != nil {
		return coded(ExitTunnelFailed, "exec_error", "cannot launch daemon: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Must comfortably exceed the daemon's tunnel-parse + readiness-probe
	// budget so the launcher never kills a daemon that is still legitimately
	// coming up: ~20s to parse the URL, then the configured ready-timeout,
	// plus a buffer.
	launchBudget := 20*time.Second + cfg.ReadyTimeout + 15*time.Second
	deadline := time.After(launchBudget)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			// The daemon exited before becoming ready; propagate its exit code
			// so the same machine-readable code surfaces to the caller.
			return coded(childExitCode(cmd), "daemon_failed",
				"daemon exited before becoming ready; see %s", state.LogPath())
		case <-deadline:
			// Don't leave a half-started daemon behind: ask it to stop, then
			// escalate to SIGKILL if it ignores us, and clear its state.
			_ = proc.Terminate(cmd.Process.Pid)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = proc.Kill(cmd.Process.Pid)
			}
			state.Remove()
			return coded(ExitTunnelFailed, "tunnel_timeout",
				"daemon did not become ready in time; see %s", state.LogPath())
		case <-tick.C:
			st, err := state.Read()
			if err == nil && st.BaseURL != "" && st.Pid == cmd.Process.Pid {
				renderSuccess(os.Stdout, cfg.JSON, buildOutput(st))
				return nil
			}
		}
	}
}

func childExitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState != nil {
		if c := cmd.ProcessState.ExitCode(); c > 0 {
			return c
		}
	}
	return ExitTunnelFailed
}

// runStop signals the running daemon, waits for it to exit, and escalates to a
// targeted SIGKILL of the daemon (and its recorded cloudflared child) if it
// ignores SIGTERM. State is only cleared once the daemon is confirmed gone, so
// a failed stop never lets a later start spawn a second instance.
func runStop(jsonMode bool) error {
	pid, err := state.ReadPid()
	if err != nil || !proc.Alive(pid) {
		state.Remove()
		return coded(ExitNotRunning, "not_running", "no running wormhole instance")
	}
	// The daemon's SIGTERM handler tears down cloudflared itself; cfPid is a
	// fallback in case the daemon dies without doing so.
	st, _ := state.Read()
	_ = proc.Terminate(pid)

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !proc.Alive(pid) {
			reapCloudflared(st)
			state.Remove()
			renderStopped(os.Stdout, jsonMode, pid)
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}

	// Escalate with a targeted kill (not a group kill, which could hit an
	// unrelated group after PID reuse).
	_ = proc.Kill(pid)
	time.Sleep(300 * time.Millisecond)
	if proc.Alive(pid) {
		return coded(ExitTunnelFailed, "stop_failed", "could not stop pid %d", pid)
	}
	reapCloudflared(st)
	state.Remove()
	renderStopped(os.Stdout, jsonMode, pid)
	return nil
}

// reapCloudflared kills the recorded cloudflared child if it somehow outlived
// the daemon, so no public tunnel is left orphaned.
func reapCloudflared(st state.State) {
	if st.CloudflaredPid > 0 && proc.Alive(st.CloudflaredPid) {
		_ = proc.Kill(st.CloudflaredPid)
	}
}

// runStatus reports the live instance or exits ExitNotRunning.
func runStatus(jsonMode bool) error {
	st, err := state.Read()
	pid, perr := state.ReadPid()
	running := perr == nil && proc.Alive(pid)
	if err != nil || !running {
		if !running {
			state.Remove()
		}
		if jsonMode {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"schema": state.Schema, "running": false,
			})
		} else {
			fmt.Fprintln(os.Stdout, "not running")
		}
		return &CodedError{Code: ExitNotRunning, ErrCode: "not_running", Msg: "not running", Silent: true}
	}
	renderSuccess(os.Stdout, jsonMode, buildOutput(st))
	return nil
}

// runPut copies a file into the running drop dir and prints its public URL.
func runPut(file, rename string, jsonMode bool) error {
	st, err := requireRunning()
	if err != nil {
		return err
	}
	info, serr := os.Stat(file)
	if serr != nil || info.IsDir() {
		return coded(ExitUsage, "bad_file", "cannot read file %q", file)
	}
	src := file
	if rename != "" {
		src = rename
	}
	name, ok := safeName(src)
	if !ok {
		return coded(ExitRejected, "bad_name",
			"refusing name %q: must be a plain filename inside the drop dir", src)
	}
	if err := copyFile(file, filepath.Join(st.DropDir, name)); err != nil {
		return coded(ExitTunnelFailed, "copy_failed", "%v", err)
	}

	o := buildOutput(st)
	var entry *fileEntry
	for i := range o.Files {
		if o.Files[i].Name == name {
			entry = &o.Files[i]
			break
		}
	}
	if entry == nil {
		// The file was copied but couldn't be re-listed; synthesise the entry
		// so --json never emits null.
		entry = &fileEntry{Name: name, URL: st.BaseURL + name}
	}
	if jsonMode {
		_ = json.NewEncoder(os.Stdout).Encode(entry)
	} else {
		fmt.Fprintln(os.Stdout, st.BaseURL+name)
	}
	return nil
}

// safeName reduces an input to a single plain filename and rejects anything
// that could escape the drop dir or collide with wormhole's bookkeeping files.
func safeName(input string) (string, bool) {
	name := filepath.Base(input)
	if name == "" || name == "." || name == ".." ||
		strings.ContainsRune(name, '/') || strings.ContainsRune(name, os.PathSeparator) ||
		strings.HasPrefix(name, ".wormhole") {
		return "", false
	}
	return name, true
}

// runURL prints the public URL for a filename already in the drop dir.
func runURL(name string, jsonMode bool) error {
	st, err := requireRunning()
	if err != nil {
		return err
	}
	clean, ok := safeName(name)
	if !ok {
		return coded(ExitRejected, "bad_name", "refusing name %q", name)
	}
	name = clean
	url := st.BaseURL + name
	if jsonMode {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{
			"schema": state.Schema, "name": name, "url": url,
		})
	} else {
		fmt.Fprintln(os.Stdout, url)
	}
	return nil
}

func requireRunning() (state.State, error) {
	st, err := state.Read()
	pid, perr := state.ReadPid()
	if err != nil || perr != nil || !proc.Alive(pid) {
		return state.State{}, coded(ExitNotRunning, "not_running",
			"no running wormhole instance; run 'wormhole start' first")
	}
	return st, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// O_EXCL + a randomised, unpredictable temp name refuses to open through a
	// pre-planted symlink, so a writer to the drop dir can't redirect the copy
	// to an arbitrary file.
	tmp := dst + ".wormhole-tmp-" + randToken()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cleanup := func() { os.Remove(tmp) }
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		cleanup()
		return err
	}
	if err := out.Close(); err != nil {
		cleanup()
		return err
	}
	// Refuse to clobber an existing symlink at the destination.
	if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		cleanup()
		return fmt.Errorf("destination %q is a symlink", filepath.Base(dst))
	}
	if err := os.Rename(tmp, dst); err != nil {
		cleanup()
		return err
	}
	return nil
}
