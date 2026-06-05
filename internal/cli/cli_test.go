package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"1024", 1024, false},
		{"1KB", 1024, false},
		{"5MB", 5 << 20, false},
		{"2GB", 2 << 30, false},
		{"512B", 512, false},
		{"1.5MB", int64(1.5 * (1 << 20)), false},
		{"-3MB", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if (err != nil) != c.err {
			t.Errorf("parseSize(%q) err = %v, wantErr %v", c.in, err, c.err)
			continue
		}
		if !c.err && got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestReorderPutsFlagsFirst(t *testing.T) {
	bools := map[string]bool{"json": true}
	got := reorder([]string{"mydir", "--ttl", "5m", "--json"}, bools)
	want := []string{"--ttl", "5m", "--json", "mydir"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reorder = %v, want %v", got, want)
	}
}

func TestReorderHandlesEqualsForm(t *testing.T) {
	got := reorder([]string{"--ttl=5m", "dir", "--json"}, map[string]bool{"json": true})
	want := []string{"--ttl=5m", "--json", "dir"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reorder = %v, want %v", got, want)
	}
}

func TestHasJSON(t *testing.T) {
	if !hasJSON([]string{"x", "--json"}) {
		t.Error("expected true for --json")
	}
	if hasJSON([]string{"x", "y"}) {
		t.Error("expected false without --json")
	}
}

func TestParseSizeZero(t *testing.T) {
	for _, in := range []string{"0", "0B", "0KB", "0MB"} {
		got, err := parseSize(in)
		if err != nil || got != 0 {
			t.Errorf("parseSize(%q) = (%d, %v), want (0, nil)", in, got, err)
		}
	}
}

func TestSafeName(t *testing.T) {
	ok := []string{"cat.png", "a.jpeg", "weird-name_1.webp"}
	for _, n := range ok {
		if got, valid := safeName(n); !valid || got != n {
			t.Errorf("safeName(%q) = (%q, %v), want accepted", n, got, valid)
		}
	}
	bad := []string{"", ".", "..", "foo/..", ".wormhole-health", ".wormhole.json"}
	for _, n := range bad {
		if _, valid := safeName(n); valid {
			t.Errorf("safeName(%q) should be rejected", n)
		}
	}
	// Names containing separators are safely reduced to their final element
	// (still confined to the drop dir), not used verbatim.
	reduced := map[string]string{"a/b": "b", "../escape": "escape", "x/y/z.png": "z.png"}
	for in, want := range reduced {
		if got, valid := safeName(in); !valid || got != want {
			t.Errorf("safeName(%q) = (%q, %v), want (%q, true)", in, got, valid, want)
		}
	}
}

func TestParseServeFlagsTokenValidation(t *testing.T) {
	var cfg serveConfig
	if err := parseServeFlags("serve", []string{"--token", "bad/token"}, &cfg); err == nil {
		t.Error("expected token with '/' to be rejected")
	}
	if err := parseServeFlags("serve", []string{"--token", "good_TOKEN-123"}, &cfg); err != nil {
		t.Errorf("valid token rejected: %v", err)
	}
}

func TestParseServeFlagsNegativeTTL(t *testing.T) {
	var cfg serveConfig
	if err := parseServeFlags("serve", []string{"--ttl", "-5m"}, &cfg); err == nil {
		t.Error("expected negative --ttl to be rejected")
	}
}

func TestCopyFileRefusesSymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "evil.png")
	if err := os.Symlink(secret, dst); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	src := filepath.Join(dir, "payload")
	if err := os.WriteFile(src, []byte("ATTACKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err == nil {
		t.Fatal("copyFile should refuse a symlink destination")
	}
	// The secret must be untouched.
	got, _ := os.ReadFile(secret)
	if string(got) != "SECRET" {
		t.Fatalf("secret was overwritten through symlink: %q", got)
	}
	// No stray temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != "" && len(e.Name()) > 20 && e.Name() != "evil.png" {
			// crude check for a leaked .wormhole-tmp-* file
			if got, _ := filepath.Match("evil.png.wormhole-tmp-*", e.Name()); got {
				t.Errorf("leaked temp file: %s", e.Name())
			}
		}
	}
}
