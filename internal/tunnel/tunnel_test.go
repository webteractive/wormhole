package tunnel

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFindURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2026-06-05 INF |  https://exciting-cat-foo.trycloudflare.com  |", "https://exciting-cat-foo.trycloudflare.com", true},
		{"Your quick Tunnel has been created! Visit it at https://abc123.trycloudflare.com", "https://abc123.trycloudflare.com", true},
		{"+--------------------------------------------------------+", "", false},
		{"https://example.com/not-a-tunnel", "", false},
		{"INF Registered tunnel connection https://9-letters-here.trycloudflare.com/path", "https://9-letters-here.trycloudflare.com", true},
	}
	for _, c := range cases {
		got, ok := FindURL(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("FindURL(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestWaitForURLFindsInStream(t *testing.T) {
	log := strings.NewReader(
		"INF starting tunnel\n" +
			"INF +----------------------------------+\n" +
			"INF |  https://random-words.trycloudflare.com  |\n" +
			"INF +----------------------------------+\n")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := WaitForURL(ctx, log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://random-words.trycloudflare.com" {
		t.Fatalf("got %q", got)
	}
}

func TestWaitForURLTimesOutWithNoMatch(t *testing.T) {
	// A reader that never yields a URL and blocks, to exercise ctx cancellation.
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := WaitForURL(ctx, pr)
	if err == nil {
		t.Fatal("expected an error on timeout")
	}
}

func TestWaitForURLEndOfStream(t *testing.T) {
	log := strings.NewReader("INF nothing useful here\nINF still nothing\n")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := WaitForURL(ctx, log)
	if err != ErrTimeout {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
}
