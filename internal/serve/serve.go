// Package serve provides the static file handler for a wormhole drop dir.
// It deliberately avoids http.FileServer: there is no directory listing, only
// files reachable at /<token>/<filename> are served, and the response always
// carries a correct Content-Type with no redirects.
package serve

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Options configures a handler.
type Options struct {
	Dir     string   // absolute drop dir; only files inside it are served
	Token   string   // required path segment, e.g. /<Token>/<file>
	Types   []string // allowed extensions (any form, with/without dot); empty = any
	MaxSize int64    // max served file size in bytes; 0 = no limit
	Health  string   // reserved filename always served (used by the readiness probe)
}

// NewHandler builds the http.Handler enforcing the token route, type/size
// limits, and path-containment rules.
func NewHandler(opt Options) http.Handler {
	prefix := "/" + opt.Token + "/"
	allowed := map[string]bool{}
	for _, t := range opt.Types {
		allowed[canonExt(t)] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		// The filename must be a single, non-traversing path element. r.URL.Path
		// is already percent-decoded, so an encoded slash collapses to "/" here
		// and is rejected.
		name := strings.TrimPrefix(r.URL.Path, prefix)
		if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}

		// Resolve symlinks and confirm the result is still inside the drop dir.
		dirReal, err := filepath.EvalSymlinks(opt.Dir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(opt.Dir, name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(resolved, dirReal+string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}

		fi, err := os.Stat(resolved)
		if err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}

		isHealth := opt.Health != "" && name == opt.Health
		if !isHealth {
			if len(allowed) > 0 && !allowed[canonExt(filepath.Ext(name))] {
				http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
				return
			}
			if opt.MaxSize > 0 && fi.Size() > opt.MaxSize {
				http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
				return
			}
		}

		f, err := os.Open(resolved)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()

		ct := "text/plain; charset=utf-8"
		if !isHealth {
			ct = contentType(name, f)
		}
		w.Header().Set("Content-Type", ct)
		// Stop browsers from re-sniffing a different (e.g. active) type.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// ServeContent honours a pre-set Content-Type, handles HEAD/range, and
		// never issues a redirect.
		http.ServeContent(w, r, name, fi.ModTime(), f)
	})
}

// contentType derives a Content-Type from the extension, falling back to a
// content sniff. It never returns an *active* document type — text/html or the
// XML family (SVG/XHTML) — which a browser could execute as script on the
// tunnel origin; those are downgraded to application/octet-stream so they are
// only ever downloaded, never rendered.
func contentType(name string, f *os.File) string {
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" {
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		_, _ = f.Seek(0, io.SeekStart)
		ct = http.DetectContentType(buf[:n])
	}
	if isActiveType(ct) {
		ct = "application/octet-stream"
	}
	return ct
}

// isActiveType reports whether a content type can carry executable markup that
// a browser would run when the URL is opened directly.
func isActiveType(ct string) bool {
	lc := strings.ToLower(ct)
	for _, p := range []string{"text/html", "image/svg", "application/xhtml", "application/xml", "text/xml"} {
		if strings.HasPrefix(lc, p) {
			return true
		}
	}
	return false
}

// canonExt normalises an extension or type token to a bare lowercase
// extension, treating jpeg/jpg as equivalent.
func canonExt(s string) string {
	s = strings.ToLower(strings.TrimPrefix(s, "."))
	if s == "jpeg" {
		return "jpg"
	}
	return s
}
