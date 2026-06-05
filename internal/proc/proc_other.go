//go:build !unix

// Best-effort fallbacks for non-unix platforms (e.g. Windows). Session
// detachment and group signalling are limited here; the tool targets
// macOS/Linux primarily.
package proc

import (
	"os"
	"os/exec"
)

func SetSession(c *exec.Cmd) {}

func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}

func Terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func Kill(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
