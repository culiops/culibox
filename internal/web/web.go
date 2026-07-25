package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

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
		writeJSON(w, http.StatusOK, map[string]string{"env_label": s.EnvLabel, "env_color": s.EnvColor})
	})
	protected.HandleFunc("/api/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, listWorkspaces(s.Home))
	})
	protected.HandleFunc("/api/fs/list", s.handleFsList)
	protected.HandleFunc("/api/fs/read", s.handleFsRead)

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
