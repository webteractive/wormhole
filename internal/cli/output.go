package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/webteractive/wormhole/internal/state"
)

// Exit codes form part of the AI-friendly contract: agents branch on the code
// rather than scraping text.
const (
	ExitOK                 = 0
	ExitUsage              = 2
	ExitCloudflaredMissing = 3
	ExitTunnelFailed       = 4
	ExitAlreadyRunning     = 5
	ExitNotRunning         = 6
	ExitRejected           = 7
)

// CodedError carries both a process exit code and a stable machine-readable
// error code. Silent suppresses the diagnostic render when the command already
// produced its own output.
type CodedError struct {
	Code    int
	ErrCode string
	Msg     string
	Silent  bool
}

func (e *CodedError) Error() string { return e.Msg }

func coded(code int, errCode, format string, a ...any) *CodedError {
	return &CodedError{Code: code, ErrCode: errCode, Msg: fmt.Sprintf(format, a...)}
}

type fileEntry struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

// output is the success payload: the live state plus the files currently in the
// drop dir.
type output struct {
	state.State
	Files []fileEntry `json:"files"`
}

func buildOutput(st state.State) output {
	return output{State: st, Files: listFiles(st)}
}

// maxListed caps how many files are enumerated for output, so a drop dir that
// fills up (or is stuffed by another writer) can't make every command do
// unbounded work.
const maxListed = 1000

// listFiles enumerates served files in the drop dir, skipping directories and
// wormhole's own bookkeeping files.
func listFiles(st state.State) []fileEntry {
	out := []fileEntry{}
	entries, err := os.ReadDir(st.DropDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if len(out) >= maxListed {
			break
		}
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".wormhole") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		ct := mime.TypeByExtension(filepath.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		out = append(out, fileEntry{
			Name:        name,
			URL:         st.BaseURL + name,
			Size:        fi.Size(),
			ContentType: ct,
		})
	}
	return out
}

func renderSuccess(w io.Writer, jsonMode bool, o output) {
	if jsonMode {
		_ = json.NewEncoder(w).Encode(o)
		return
	}
	fmt.Fprintf(w, "drop dir : %s\n", o.DropDir)
	fmt.Fprintf(w, "base url : %s\n", o.BaseURL)
	if o.ExpiresAt != "" {
		fmt.Fprintf(w, "expires  : %s\n", o.ExpiresAt)
	}
	if len(o.Files) == 0 {
		fmt.Fprintf(w, "files    : (none yet — drop files into the drop dir)\n")
		return
	}
	for i, f := range o.Files {
		label := "files    :"
		if i > 0 {
			label = "         :"
		}
		fmt.Fprintf(w, "%s %s\n", label, f.URL)
	}
}

func renderError(w io.Writer, jsonMode bool, e *CodedError) {
	if jsonMode {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema": state.Schema,
			"error":  map[string]string{"code": e.ErrCode, "message": e.Msg},
		})
		return
	}
	fmt.Fprintf(w, "wormhole: %s\n", e.Msg)
}

func renderStopped(w io.Writer, jsonMode bool, pid int) {
	if jsonMode {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema":  state.Schema,
			"stopped": true,
			"pid":     pid,
		})
		return
	}
	fmt.Fprintf(w, "stopped wormhole (pid %d)\n", pid)
}
