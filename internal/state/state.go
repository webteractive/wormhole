// Package state owns the on-disk footprint of a running wormhole instance:
// the pidfile, the daemon logfile, and a small JSON snapshot of the live
// tunnel so other subcommands (status, put, url) can find it without
// re-parsing cloudflared.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Schema is the stable identifier embedded in every machine-readable payload.
const Schema = "wormhole/v1"

// State is the snapshot persisted while an instance is live. It doubles as the
// core of the JSON output contract.
type State struct {
	Schema         string `json:"schema"`
	BaseURL        string `json:"base_url"`
	DropDir        string `json:"drop_dir"`
	Port           int    `json:"port"`
	Token          string `json:"token"`
	TTLSeconds     int    `json:"ttl_seconds"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	Pid            int    `json:"pid"`
	CloudflaredPid int    `json:"cloudflared_pid,omitempty"`
}

// Dir resolves the directory holding the pidfile, logfile, and state snapshot.
// WORMHOLE_STATE_DIR overrides everything (used by tests); otherwise it follows
// XDG_STATE_HOME, falling back to ~/.wormhole.
func Dir() string {
	if d := os.Getenv("WORMHOLE_STATE_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "wormhole")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".wormhole")
	}
	return filepath.Join(os.TempDir(), "wormhole")
}

func PidPath() string   { return filepath.Join(Dir(), "wormhole.pid") }
func LogPath() string   { return filepath.Join(Dir(), "wormhole.log") }
func statePath() string { return filepath.Join(Dir(), "wormhole.json") }

// ensureDir creates the state directory with owner-only permissions: it holds
// the daemon log, which records the secret tokenised URL.
func ensureDir() error { return os.MkdirAll(Dir(), 0o700) }

// Write atomically persists the state snapshot (stamping the schema).
func Write(st State) error {
	if err := ensureDir(); err != nil {
		return err
	}
	st.Schema = Schema
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath())
}

// Read loads the persisted state snapshot.
func Read() (State, error) {
	var st State
	b, err := os.ReadFile(statePath())
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(b, &st)
	return st, err
}

// WritePid records the supervisor pid.
func WritePid(pid int) error {
	if err := ensureDir(); err != nil {
		return err
	}
	return os.WriteFile(PidPath(), []byte(strconv.Itoa(pid)), 0o600)
}

// AcquirePid atomically claims the pidfile as a startup lock. It returns
// acquired=true only if it created the pidfile (O_EXCL); if the file already
// exists it returns acquired=false plus the pid recorded in it, letting the
// caller decide whether that owner is alive (refuse) or stale (clear & retry).
// This closes the check-then-write race between two concurrent starts.
func AcquirePid(pid int) (acquired bool, existing int, err error) {
	if err = ensureDir(); err != nil {
		return false, 0, err
	}
	f, err := os.OpenFile(PidPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			existing, _ = ReadPid()
			return false, existing, nil
		}
		return false, 0, err
	}
	defer f.Close()
	_, err = f.WriteString(strconv.Itoa(pid))
	return err == nil, 0, err
}

// LogFile opens (truncating) the daemon log with owner-only permissions.
func LogFile() (*os.File, error) {
	if err := ensureDir(); err != nil {
		return nil, err
	}
	return os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}

// ReadPid returns the recorded supervisor pid.
func ReadPid() (int, error) {
	b, err := os.ReadFile(PidPath())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// Remove deletes the pidfile and state snapshot, ignoring missing files.
func Remove() {
	os.Remove(statePath())
	os.Remove(PidPath())
}
