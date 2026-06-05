//go:build unix

// Package proc holds the small platform-specific bits of process management:
// detaching a daemon into its own session, checking liveness, and signalling.
package proc

import (
	"os"
	"os/exec"
	"syscall"
)

// SetSession makes the command start in a new session (and process group), so
// it survives the parent exiting and can be group-killed as a unit.
func SetSession(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Alive reports whether a process with the given pid currently exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 performs error checking without actually sending a signal.
	return p.Signal(syscall.Signal(0)) == nil
}

// Terminate asks a process to shut down gracefully (SIGTERM).
func Terminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// Kill force-kills a single process (SIGKILL). We target individual pids
// rather than a process group: a group kill keyed on a possibly-reused pid
// could take down an unrelated group.
func Kill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
