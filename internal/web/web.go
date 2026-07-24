package web

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/culiops/culiops-agent-console/internal/auth"
	"github.com/culiops/culiops-agent-console/internal/config"
)

//go:embed static
var staticFS embed.FS

// Server wires the API, dashboard, and terminal WebSocket.
type Server struct {
	API      *API
	Cookie   *auth.CookieAuth
	Agents   []string
	Terminal http.Handler
}

// AgentNames extracts agent names for the /api/agents endpoint.
func AgentNames(c config.Config) []string {
	names := make([]string, 0, len(c.Agents))
	for _, a := range c.Agents {
		names = append(names, a.Name)
	}
	return names
}

// Handler builds the root handler. Everything except /ws/ sits behind
// basic-auth; /ws/ is gated by the signed cookie instead, because browsers
// cannot set an Authorization header on new WebSocket().
func (s *Server) Handler(user, pass string) http.Handler {
	protected := http.NewServeMux()
	protected.HandleFunc("/api/sessions", s.API.handleSessions)
	protected.HandleFunc("/api/sessions/", s.API.handleSession)
	protected.HandleFunc("/api/agents", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.Agents)
	})

	sub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(sub))
	protected.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Mint the WS auth cookie on the authed dashboard load.
		if r.URL.Path == "/" && s.Cookie != nil {
			s.Cookie.Issue(w)
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
