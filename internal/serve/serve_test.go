package serve

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const token = "tok123"

// onePixelPNG is a minimal valid PNG (used so extension and sniff agree).
var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

func newServer(t *testing.T, opt Options) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	opt.Dir = dir
	opt.Token = token
	srv := httptest.NewServer(NewHandler(opt))
	t.Cleanup(srv.Close)
	return srv, dir
}

func write(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	// Disable redirects so a redirect would surface as a 3xx rather than being
	// transparently followed.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestServesFileWithCorrectContentType(t *testing.T) {
	srv, dir := newServer(t, Options{})
	write(t, dir, "pixel.png", onePixelPNG)

	resp := get(t, srv, "/"+token+"/pixel.png")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
}

func TestNoContentTypeIsNeverHTML(t *testing.T) {
	srv, dir := newServer(t, Options{})
	// HTML-looking bytes with no extension would sniff as text/html.
	write(t, dir, "weird", []byte("<html><body>hi</body></html>"))

	resp := get(t, srv, "/"+token+"/weird")
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, must not be text/html", ct)
	}
}

func TestDirectoryAndBareTokenAre404(t *testing.T) {
	srv, dir := newServer(t, Options{})
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/" + token + "/", "/" + token + "/sub", "/", "/other/x"} {
		resp := get(t, srv, p)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestTraversalBlocked(t *testing.T) {
	srv, dir := newServer(t, Options{})
	// secret lives outside the drop dir
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(secret) })

	for _, p := range []string{
		"/" + token + "/../secret.txt",
		"/" + token + "/..%2fsecret.txt",
		"/" + token + "/%2e%2e/secret.txt",
	} {
		resp := get(t, srv, p)
		body := resp.StatusCode
		resp.Body.Close()
		if body == 200 {
			t.Errorf("GET %s leaked a file (status 200)", p)
		}
	}
}

func TestSymlinkEscapeBlocked(t *testing.T) {
	srv, dir := newServer(t, Options{})
	secret := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(secret, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(secret) })
	if err := os.Symlink(secret, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	resp := get(t, srv, "/"+token+"/link.txt")
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatalf("symlink escape served a file outside the drop dir")
	}
}

func TestTypeAllowlistRejects(t *testing.T) {
	srv, dir := newServer(t, Options{Types: []string{"png", "jpeg"}})
	write(t, dir, "pixel.png", onePixelPNG)
	write(t, dir, "notes.txt", []byte("hello"))

	if resp := get(t, srv, "/"+token+"/pixel.png"); resp.StatusCode != 200 {
		resp.Body.Close()
		t.Fatalf("png should be allowed, got %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	resp := get(t, srv, "/"+token+"/notes.txt")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("txt status = %d, want 415", resp.StatusCode)
	}
}

func TestMaxSizeRejects(t *testing.T) {
	srv, dir := newServer(t, Options{MaxSize: 8})
	write(t, dir, "big.bin", []byte("0123456789"))
	resp := get(t, srv, "/"+token+"/big.bin")
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

func TestHealthFileBypassesTypeAndSize(t *testing.T) {
	srv, dir := newServer(t, Options{Types: []string{"png"}, MaxSize: 2, Health: ".wormhole-health"})
	write(t, dir, ".wormhole-health", []byte("a-much-longer-nonce-than-two-bytes"))
	resp := get(t, srv, "/"+token+"/.wormhole-health")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
}

func TestSvgIsDowngradedAndNosniff(t *testing.T) {
	srv, dir := newServer(t, Options{})
	write(t, dir, "icon.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	resp := get(t, srv, "/"+token+"/icon.svg")
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); strings.Contains(strings.ToLower(ct), "svg") {
		t.Fatalf("SVG must not be served as an active type, got %q", ct)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", ct)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected X-Content-Type-Options: nosniff")
	}
}

func TestNosniffOnNormalFile(t *testing.T) {
	srv, dir := newServer(t, Options{})
	write(t, dir, "pixel.png", onePixelPNG)
	resp := get(t, srv, "/"+token+"/pixel.png")
	defer resp.Body.Close()
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected nosniff on every served file")
	}
}

func TestNoRedirectEmitted(t *testing.T) {
	srv, dir := newServer(t, Options{})
	write(t, dir, "pixel.png", onePixelPNG)
	resp := get(t, srv, "/"+token+"/pixel.png")
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		t.Fatalf("unexpected redirect: %d -> %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}
