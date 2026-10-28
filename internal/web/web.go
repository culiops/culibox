package web

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/analytics"
	"github.com/culiops/agent-box/internal/auth"
	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/secretchain"
	"github.com/culiops/agent-box/internal/secretstore"
	"github.com/culiops/agent-box/internal/session"
)

//go:embed static
var staticFS embed.FS

// Server wires the API, dashboard, and terminal WebSocket.
type Server struct {
	API      *API
	Profiles *ProfileManager
	// Session issues/verifies the browser session cookie; Tokens holds bearer
	// access tokens. Together they back authGuard, which replaces basic-auth.
	Session *auth.Session
	Tokens  *auth.TokenStore
	// Password is the master password checked at /login (single operator; sourced
	// from the env var named by config's password_env). Never logged.
	Password string
	Home     string // for the workspace picker ($HOME + subfolders)
	// Protected are the directories a boxed workspace must not overlap (data
	// root, config and profiles directories). The picker marks a directory that
	// overlaps one unboxable, matching the daemon's refusal.
	Protected []box.ProtectedDir
	Terminal  http.Handler
	EnvLabel  string // shown as the UI env badge; empty → no badge
	EnvColor  string // CSS color for the env badge/strip
	// RepliesEnabled mirrors config replies_enabled and is published in
	// /api/meta so the client fails closed (reply UI off until it says true).
	RepliesEnabled bool
	// BoxRuntime is the detected isolation runtime ("runc"/"sysbox-runc"), or ""
	// when containment is unavailable and every session runs on the host tier.
	BoxRuntime string
	// ShellProfile is the profile the UI's quick-shell (+sh) button spawns —
	// the auto-detected $SHELL seed, or the user's own profile for that binary.
	// Empty → the UI offers no quick shell.
	ShellProfile string
	// ShellBin is the binary ShellProfile must still resolve to. A runtime-stored
	// profile can take over a same-named seed (ProfileManager.merged), so the name
	// is re-checked against this on every /api/meta rather than trusted from boot.
	ShellBin string

	// AllowedHosts is every Host header value the instance answers to (from
	// config.HostAllowlist). Empty refuses every request: the guard fails closed.
	AllowedHosts []string

	// RateLimitsFile is the account 5h/weekly usage snapshot (produced by a
	// statusLine hook); read fresh on every /api/meta. Empty → no limits in the UI.
	RateLimitsFile string

	// BoxFiles reads a file each boxed session wrote in its box home (the
	// session manager's BoxFiles). Nil → no box-written data. Its content is
	// untrusted and is sanitized before it reaches the quota display.
	BoxFiles func(rel string, max int64) []session.BoxFile

	// BoxTrees reports, per fs request, the host trees a box can write: denied
	// ones are refused, contained ones (boxed workspaces) are served only through
	// os.Root so a box-planted symlink cannot reach host files. Nil → no boxed
	// trees (host behaviour).
	BoxTrees func() BoxTrees

	// Commit is the build's commit, published in /api/meta so the UI and a bug
	// report can name the build.
	Commit string
	// Version is the release (git describe of a vX.Y.Z tag), shown in the
	// header and the About tab. Empty when the build was not stamped.
	Version string
	// BugReport builds the redacted diagnostic bundle (markdown plus the
	// prefilled issue link) behind POST /api/bugreport. Nil → that route answers
	// 503. It returns ErrBugReportBusy while an earlier build is still running;
	// the builder, not the handler, owns that in-flight state.
	BugReport func(ctx context.Context) (markdown, issueURL string, err error)

	// Accounts is the agent-account registry behind /api/accounts. Nil → the
	// feature is off: no routes, and only the default account's quota is shown.
	Accounts *account.Store
	// SecretOK reports whether a secret NAME resolves (never its value); it only
	// feeds an account's login_state.
	SecretOK func(name string) bool

	// Store is the encrypted store behind /api/store. Nil → the routes are not
	// registered. Never a typed nil: the daemon leaves it unset without a store.
	Store StoreService
	// StoreAudit receives unlock_rate_limited (best effort); the store records
	// its own unlocks and locks.
	StoreAudit secretstore.Recorder
	// StoreInUse: whether a configured reference reads the store, asked per
	// request (a credential save can change it); nil = false. StoreUnlockTTL is how
	// long a web unlock lasts. StorePlaintextCopies are secret NAMES (never
	// values) that are in the store and also set in the daemon's environment.
	StoreInUse           func() bool
	StoreUnlockTTL       time.Duration
	StorePlaintextCopies []string
	storeLimit           unlockLimiter
	// SeccompProbe reports whether Docker's default seccomp profile is on, the
	// same probe the launcher's SeccompGate uses. nil = the host has no docker
	// gate (macOS, or no containment), so store init is not held back.
	SeccompProbe func() (bool, error)

	// Creds owns the web-defined credentials; nil → the /api/credentials
	// routes are not registered. CredAWS is the host's AWS signer behind the
	// roles, check and sign-in routes: nil when no broker is running (those
	// routes answer 409 no_broker), and never a typed nil. CredAudit receives
	// the AWS-setup audit rows. credLimit is the shared budget of the routes
	// that call AWS.
	Creds *CredentialManager
	// SecretCheck says whether a profile's legacy secret NAME resolves, without
	// handing back the value (secretchain.Chain.Check). nil = every name reads
	// "missing".
	SecretCheck func(name string) (secretchain.Presence, error)
	CredAWS     AWSCredService
	CredAudit   CredRecorder
	credLimit   callLimiter

	// Analytics serves the token/prompt analytics endpoints and view. Nil → the
	// feature is disabled: no routes registered, no data shown.
	Analytics *analytics.Service

	// Audit reads audit.db for the Audit page. Nil → the feature is off: no
	// routes registered, "audit": false in /api/meta. AuditRetentionDays is the
	// egress pruning policy (0 = kept forever) the summary reports to the footer.
	Audit              AuditReader
	AuditRetentionDays int

	// MaxUploadBytes caps a single uploaded file; MaxArchiveBytes/Files cap a
	// directory download. Zero means the built-in default in every case — unset
	// must never be read as "unlimited".
	MaxUploadBytes  int64
	MaxArchiveBytes int64
	MaxArchiveFiles int

	// idx is the lazily-built file index behind /api/fs/search (see fssearch.go).
	idxMu sync.Mutex
	idx   *fileIndex

	// prefIdx holds file indexes rooted at "preferred" directories (the
	// focused session's cwd, see /api/fs/search?prefer=), least recently used
	// evicted past maxPreferIndexes. prefOrder is oldest first.
	prefMu    sync.Mutex
	prefIdx   map[string]*fileIndex
	prefOrder []string
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

// Handler builds the root handler. Every route except /login sits behind
// authGuard, which accepts EITHER a valid session cookie OR a valid bearer
// token. /ws/ uses the same guard (browsers present the cookie; native clients
// present a bearer token). /login is the only public route.
func (s *Server) Handler() http.Handler {
	protected := http.NewServeMux()
	protected.HandleFunc("/api/sessions", s.API.handleSessions)
	protected.HandleFunc("/api/sessions/", s.API.handleSession)
	protected.HandleFunc("/api/profiles", s.handleProfiles)
	protected.HandleFunc("/api/profiles/", s.handleProfile)
	if s.Accounts != nil {
		protected.HandleFunc("/api/accounts", s.handleAccounts)
		protected.HandleFunc("/api/accounts/", s.handleAccount)
	}
	if s.Store != nil {
		protected.HandleFunc("/api/store", s.handleStore)
		protected.HandleFunc("/api/store/unlock", s.handleStoreUnlock)
		protected.HandleFunc("/api/store/lock", s.handleStoreLock)
		protected.HandleFunc("/api/store/init", s.handleStoreInit)
	}
	if s.Creds != nil {
		protected.HandleFunc("/api/credentials", s.handleCredentials)
		protected.HandleFunc("/api/credentials/aws", s.handleCredAWS)
		protected.HandleFunc("/api/credentials/aws/", s.handleCredAWS)
		protected.HandleFunc("/api/credentials/templates", s.handleCredTemplates)
		protected.HandleFunc("/api/credentials/tokens", s.handleCredTokens)
		protected.HandleFunc("/api/credentials/tokens/", s.handleCredTokens)
		protected.HandleFunc("/api/credentials/github-apps", s.handleCredGitHubApps)
		protected.HandleFunc("/api/credentials/github-apps/", s.handleCredGitHubApps)
		protected.HandleFunc("/api/store/values/", s.handleStoreValue)
	}
	protected.HandleFunc("/api/options", s.handleOptions)
	protected.HandleFunc("/api/tokens", s.handleTokens)
	protected.HandleFunc("/api/tokens/", s.handleToken)
	protected.HandleFunc("/api/bugreport", s.handleBugReport)
	protected.HandleFunc("/logout", s.handleLogout)
	protected.HandleFunc("/api/meta", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{
			"env_label":     s.EnvLabel,
			"env_color":     s.EnvColor,
			"shell_profile": s.quickShell(),
			"box_runtime":   s.BoxRuntime,
			"analytics":     s.Analytics != nil,
			"audit":         s.Audit != nil,
			"replies":       s.RepliesEnabled,
			"commit":        s.Commit,
			"version":       s.Version,
		}
		// The App names the profile editor offers; [] when none are defined.
		meta["github_apps"] = []string{}
		if s.Profiles != nil {
			meta["github_apps"] = s.Profiles.GitHubAppNames()
		}
		// The static_tokens entries the Tokens picker offers (names and shape,
		// never a value or its reference); [] when none are defined.
		meta["static_tokens"] = []TokenMeta{}
		if s.Profiles != nil {
			meta["static_tokens"] = s.Profiles.StaticTokenMeta()
		}
		// The aws_connections the profile editor's picker offers (names and
		// shape, never a source profile or a role); [] when none are defined.
		meta["aws_connections"] = []AWSConnectionMeta{}
		if s.Profiles != nil {
			meta["aws_connections"] = s.Profiles.AWSConnectionMetaList()
		}
		// Per-account 5h/weekly usage, read fresh (tiny files). Omitted when no
		// account has data so the client shows nothing rather than an empty widget.
		if m := s.rateLimitsByAccount(); len(m) > 0 {
			meta["rate_limits"] = m
		}
		writeJSON(w, http.StatusOK, meta)
	})
	protected.HandleFunc("/api/workspaces", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, listWorkspaces(s.Home, s.Protected))
	})
	protected.HandleFunc("/api/fs/list", s.handleFsList)
	protected.HandleFunc("/api/fs/read", s.handleFsRead)
	protected.HandleFunc("/api/fs/search", s.handleFsSearch)
	protected.HandleFunc("/api/fs/upload", s.handleFsUpload)
	protected.HandleFunc("/api/fs/archive", s.handleFsArchive)
	protected.HandleFunc("/api/fs/mkdir", s.handleFsMkdir)
	protected.HandleFunc("/api/fs/rename", s.handleFsRename)
	protected.HandleFunc("/api/fs/delete", s.handleFsDelete)
	protected.HandleFunc("/api/fs/write", s.handleFsWrite)

	// Analytics routes only exist when the feature is enabled; the POST /ingest
	// inherits csrfGuard's same-origin check like every other state-changing call.
	if s.Audit != nil {
		protected.HandleFunc("/api/audit", s.handleAudit)
		protected.HandleFunc("/api/audit/summary", s.handleAuditSummary)
		protected.HandleFunc("/api/audit.csv", s.handleAuditCSV)
	}
	if s.Analytics != nil {
		protected.HandleFunc("/api/analytics/summary", s.handleAnalyticsSummary)
		protected.HandleFunc("/api/analytics/models", s.handleAnalyticsModels)
		protected.HandleFunc("/api/analytics/projects", s.handleAnalyticsProjects)
		protected.HandleFunc("/api/analytics/sessions", s.handleAnalyticsSessions)
		protected.HandleFunc("/api/analytics/session-costs", s.handleAnalyticsSessionCosts)
		protected.HandleFunc("/api/analytics/tools", s.handleAnalyticsTools)
		protected.HandleFunc("/api/analytics/ingest", s.handleAnalyticsIngest)
	}

	sub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(sub))
	protected.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// App HTML/CSS/JS change on every rebuild → force revalidation, otherwise
		// browsers serve stale assets until a manual hard-refresh. The vendored
		// /xterm/* and /vendor/* bundles are immutable and stay cacheable.
		pth := r.URL.Path
		app := strings.HasSuffix(pth, ".js") || strings.HasSuffix(pth, ".css")
		if pth == "/" || (app &&
			!strings.HasPrefix(pth, "/xterm/") && !strings.HasPrefix(pth, "/vendor/")) {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})

	root := http.NewServeMux()
	// Public login surface. GET serves the static form; POST verifies the
	// password. The exact "/login" pattern wins over the "/" subtree for both.
	root.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.ServeFileFS(w, r, staticFS, "static/login.html")
			return
		}
		s.handleLogin(w, r)
	})
	// The login page is public, so the stylesheets it links must be too, or the
	// <link> is answered by authGuard's redirect and the page renders unstyled.
	// Only tokens.css, primitives.css and desktop.css (plus the one Inter file
	// desktop.css's @font-face names) are exposed: a colour palette and type,
	// which the login page's own appearance already reveals. app.css stays
	// protected. desktop.css carries UI selector names but no data, and the
	// source is public.
	for _, name := range []string{"tokens.css", "primitives.css", "desktop.css", "vendor/inter/InterVariable.woff2"} {
		root.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			if strings.HasSuffix(name, ".woff2") {
				w.Header().Set("Content-Type", "font/woff2")
			}
			http.ServeFileFS(w, r, staticFS, "static/"+name)
		})
	}
	root.Handle("/ws/", s.authGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Terminal.ServeHTTP(w, r)
	})))
	root.Handle("/", s.authGuard(csrfGuard(protected, s.API.auditCSRFRefusal)))
	// Outermost: every route, static and /login included, is Host-checked first.
	return newHostGuard(s.AllowedHosts, nil).wrap(root)
}

// csrfGuard rejects cross-site requests that could change state.
//
// A session cookie is auto-attached by the browser on cross-site requests, and
// unlike a token there is no header the attacker must forge — so cookie-auth
// writes need a same-origin check. Bearer-token writes carry no CSRF risk (the
// browser never auto-attaches an Authorization header), so they are exempt;
// this is what lets a native client POST without a matching Origin. Most of the
// JSON API is also incidentally safe because a JSON content type forces a CORS
// preflight, but multipart/form-data (the upload) is safelisted and needs none,
// so the guard belongs on the mux regardless of content type.
//
// Reads are deliberately not blocked: a cross-site GET cannot change state, and
// the response is opaque to the caller anyway.
//
// onRefuse, when set, is told about every request the guard itself turns away
// (before any handler runs), so a refusal the handler would have audited is not
// lost. Behaviour for the request is unchanged by it.
func csrfGuard(next http.Handler, onRefuse func(*http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if credKindOf(r) == credBearer {
			next.ServeHTTP(w, r)
			return
		}
		if !sameOrigin(r) {
			if onRefuse != nil {
				onRefuse(r)
			}
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}
