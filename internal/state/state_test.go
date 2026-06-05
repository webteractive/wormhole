package state

import (
	"os"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	t.Setenv("WORMHOLE_STATE_DIR", t.TempDir())

	in := State{
		BaseURL: "https://x.trycloudflare.com/tok/", DropDir: "/tmp/drop",
		Port: 4321, Token: "tok", TTLSeconds: 600, Pid: 1234,
	}
	if err := Write(in); err != nil {
		t.Fatal(err)
	}
	got, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != Schema {
		t.Errorf("schema = %q, want %q", got.Schema, Schema)
	}
	if got.BaseURL != in.BaseURL || got.Port != in.Port || got.Token != in.Token || got.Pid != in.Pid {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestPidRoundTripAndRemove(t *testing.T) {
	t.Setenv("WORMHOLE_STATE_DIR", t.TempDir())

	if err := WritePid(4242); err != nil {
		t.Fatal(err)
	}
	pid, err := ReadPid()
	if err != nil || pid != 4242 {
		t.Fatalf("ReadPid = (%d, %v)", pid, err)
	}
	_ = Write(State{Pid: 4242})

	Remove()
	if _, err := ReadPid(); err == nil {
		t.Error("pidfile should be gone after Remove")
	}
	if _, err := Read(); err == nil {
		t.Error("state should be gone after Remove")
	}
}

func TestAcquirePidIsExclusive(t *testing.T) {
	t.Setenv("WORMHOLE_STATE_DIR", t.TempDir())

	acquired, _, err := AcquirePid(1111)
	if err != nil || !acquired {
		t.Fatalf("first AcquirePid = (%v, %v), want acquired", acquired, err)
	}
	acquired2, existing, err := AcquirePid(2222)
	if err != nil {
		t.Fatal(err)
	}
	if acquired2 {
		t.Fatal("second AcquirePid should not acquire while pidfile exists")
	}
	if existing != 1111 {
		t.Fatalf("existing pid = %d, want 1111", existing)
	}

	// After clearing, it can be re-acquired.
	Remove()
	acquired3, _, err := AcquirePid(3333)
	if err != nil || !acquired3 {
		t.Fatalf("re-acquire after Remove = (%v, %v), want acquired", acquired3, err)
	}
}

func TestLogFilePermsAreOwnerOnly(t *testing.T) {
	t.Setenv("WORMHOLE_STATE_DIR", t.TempDir())
	f, err := LogFile()
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	fi, err := os.Stat(LogPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("log perms = %o, want 600 (it records the secret URL)", perm)
	}
}

func TestStateDirOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WORMHOLE_STATE_DIR", dir)
	if Dir() != dir {
		t.Fatalf("Dir() = %q, want %q", Dir(), dir)
	}
	_ = os.Unsetenv("WORMHOLE_STATE_DIR")
}
