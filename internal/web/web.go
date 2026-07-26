package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/culiops/culiops-agent-console/internal/auth"
)

//go:embed static
var staticFS embed.FS

// Server wires the API, dashboard, and terminal WebSocket.
type Server struct {
	API      *API
	Cookie   *auth.CookieAuth
	Profiles *ProfileManager
	Home     string // for the workspace picker ($HOME + subfolders)
	Terminal http.Handler
	EnvLabel string // shown as the UI env badge; empty → no badge
	EnvColor string // CSS color for the env badge/strip
	// ShellProfile is the profile the UI's quick-shell (+sh) button spawns —
	// the auto-detected $SHELL seed, or the user's own profile for that binary.
	// Empty → the UI offers no quick shell.
	ShellProfile string
	// ShellBin is the binary ShellProfile must still resolve to. A runtime-stored
	// profile can take over a same-named seed (ProfileManager.merged), so the name
	// is re-checked against this on every /api/meta rather than trusted from boot.
	ShellBin string

	// MaxUploadBytes caps a single uploaded file; MaxArchiveBytes/Files cap a
	// directory download. Zero means the built-in default in every case — unset
	// must never be read as "unlimited".
	MaxUploadBytes  int64
	MaxArchiveBytes int64
	MaxArchiveFiles int

	// idx is the lazily-built file index behind /api/fs/search (see fssearch.go).
	idxMu sync.Mutex
	idx   *fileIndex
}

// QuickShell exposes the verified quick-shell name for startup diagnostics.
func (s *Server) QuickShell() string { return s.quickShell() }

// quickShell returns the quick-shell profile name, or "" when it no longer runs
// the detected shell binary — the +sh button must never silently launch something
// else because a stored profile took the name.
//
// The comparison is by BASENAME, matching config.WithDetectedShell: that function
// deliberately treats an agent whose bin is a bare "zsh" as the user's own profile
// for /usr/bin/zsh, so full-path equality here would reject selections it just made.
//
// Fails closed: without the pieces needed to verify, offer no quick shell.
func (s *Server) quickShell() string {
	if s.ShellProfile == "" || s.Profiles == nil || s.ShellBin == "" {
		return ""
	}
	p, ok := s.Profiles.Get(s.ShellProfile)
	if !ok || filepath.Base(p.Bin) != filepath.Base(s.ShellBin) {
		return ""
	}
	return s.ShellProfile
}

// Handler builds the root handler. Everything except /ws/ sits behind
// basic-auth; /ws/ is gated by the signed cookie instead, because browsers
// cannot set an Authorization header on new WebSocket().
func (s *Server) Handler(user, pass string) http.Handler {
	protected := http.NewServeMux()
	protected.HandleFunc("/api/sessions", s.API.handleSessions)
	protected.HandleFunc("/api/sessions/", s.API.handleSession)
	protected.HandleFunc("/api/profiles", s.handleProfiles)
	protected.HandleFunc("/api/profiles/", s.handleProfile)
	protected.HandleFunc("/api/options", s.handleOptions)
	protected.HandleFunc("/api/meta", func(w http.ResponseWriter, r *http.Request) {
		// Re-mint the WS cookie here too, not just on the dashboard load. /ws is
		// cookie-only gated, so a live tab whose cookie went stale (regenerated
		// secret) could otherwise never reconnect without a manual page reload.
		// Same basic-auth gate as "/", so this is not a wider trust boundary.
		if s.Cookie != nil {
			s.Cookie.Issue(w)
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"env_label":     s.EnvLabel,
			"env_color":     s.EnvColor,
			"shell_profile": s.quickShell(),
		})
	})
	protected.HandleFunc("/api/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, listWorkspaces(s.Home))
	})
	protected.HandleFunc("/api/fs/list", s.handleFsList)
	protected.HandleFunc("/api/fs/read", s.handleFsRead)
	protected.HandleFunc("/api/fs/search", s.handleFsSearch)
	protected.HandleFunc("/api/fs/upload", s.handleFsUpload)
	protected.HandleFunc("/api/fs/archive", s.handleFsArchive)

	sub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(sub))
	protected.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Mint the WS auth cookie on the authed dashboard load.
		if r.URL.Path == "/" && s.Cookie != nil {
			s.Cookie.Issue(w)
		}
		// The app's own files change on every rebuild, so force revalidation —
		// otherwise browsers serve stale JS/HTML until a manual hard-refresh. The
		// vendored /xterm/* bundle is immutable and left cacheable.
		// App HTML/JS change on every rebuild → force revalidation. The vendored
		// /xterm/* and /vendor/* bundles are immutable and stay cacheable.
		pth := r.URL.Path
		if pth == "/" || (strings.HasSuffix(pth, ".js") &&
			!strings.HasPrefix(pth, "/xterm/") && !strings.HasPrefix(pth, "/vendor/")) {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})

	root := http.NewServeMux()
	root.Handle("/", auth.BasicAuth(user, pass, protected))
	root.HandleFunc("/ws/", func(w http.ResponseWriter, r *http.Request) {
		if s.Cookie != nil && !s.Cookie.Valid(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.Terminal.ServeHTTP(w, r)
	})
	return root
}
