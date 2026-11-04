package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/culiops/culibox/internal/hostmatch"
	"github.com/culiops/culibox/internal/secretstore"
)

// KeyRef says where a secret's bytes live: a file on the host or an
// environment variable of the daemon. The bytes are resolved at use and never
// enter a box as a reference. "store" names a value in the agent-box encrypted
// store by id.
type KeyRef struct {
	Type string `json:"type"`           // "file" | "env" | "store"
	Path string `json:"path,omitempty"` // file: absolute path
	Var  string `json:"var,omitempty"`  // env: variable name
	ID   string `json:"id,omitempty"`   // store: value id
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidEnvName reports whether s is a legal environment variable name.
func ValidEnvName(s string) bool { return envNameRe.MatchString(s) }

// Validate checks the reference's shape; it never reads the value.
func (r KeyRef) Validate() error {
	switch r.Type {
	case "file":
		if r.Var != "" {
			return errors.New(`a "file" reference must not set var`)
		}
		if r.ID != "" {
			return errors.New(`a "file" reference must not set id`)
		}
		if !filepath.IsAbs(r.Path) {
			return fmt.Errorf("path %q must be an absolute path", r.Path)
		}
	case "env":
		if r.Path != "" {
			return errors.New(`an "env" reference must not set path`)
		}
		if r.ID != "" {
			return errors.New(`an "env" reference must not set id`)
		}
		if !ValidEnvName(r.Var) {
			return fmt.Errorf("env var %q is not a valid variable name", r.Var)
		}
		if strings.HasPrefix(r.Var, "AB_") {
			return fmt.Errorf("env var %q is reserved: the AB_ prefix holds agent-box's own settings", r.Var)
		}
	case "store":
		if r.Path != "" || r.Var != "" {
			return errors.New(`a "store" reference must not set path or var`)
		}
		if !secretstore.ValidID(r.ID) {
			return fmt.Errorf("store id %q must be letters, digits, _ and - (starting with a letter or digit, at most 64)", r.ID)
		}
	default:
		return fmt.Errorf("unsupported reference type %q (want \"file\", \"env\" or \"store\")", r.Type)
	}
	return nil
}

// String names the reference for messages. It is never a value.
func (r KeyRef) String() string {
	switch r.Type {
	case "env":
		return "env " + r.Var
	case "store":
		return "store " + r.ID
	}
	return r.Type + " " + r.Path
}

// GitHubApp is a named GitHub App connection, declared once in config.json
// (github_apps) and referenced by name from a profile's binding.
type GitHubApp struct {
	Name       string `json:"name"`
	ClientID   string `json:"client_id"`
	PrivateKey KeyRef `json:"private_key"`
}

// GitHubOwner scopes a binding to named repositories under one account or
// organisation. Exactly one of App (a minted installation token) and Token (a
// static_tokens entry holding a personal access token) says where the
// credential comes from.
type GitHubOwner struct {
	Owner string   `json:"owner"`
	App   string   `json:"app"`
	Repos []string `json:"repos"`
	Token string   `json:"token,omitempty"`
}

// GitHubBinding gives a boxed profile GitHub access through the credential
// broker: read by default, write only on the keys GitHubWritablePermissions
// lists. Every field shapes the scope, so all of them go into Hash and
// therefore into the box key.
type GitHubBinding struct {
	Owners      []GitHubOwner     `json:"owners"`
	Permissions map[string]string `json:"permissions,omitempty"` // empty = DefaultGitHubPermissions()
	// EgressAllow adds hosts (exact or *.suffix) to the egress proxy's default
	// allowlist for this binding's boxes.
	EgressAllow []string `json:"egress_allow,omitempty"`
}

const maxGitHubRepos = 100

var (
	githubOwnerRe    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	githubRepoRe     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	githubClientIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// githubPermissionKeys are the only permissions a binding may ask for. Each is
// "read" at most, except the ones GitHubWritablePermissions lists.
var githubPermissionKeys = []string{"actions", "checks", "contents", "pull_requests", "issues", "metadata", "statuses"}

// githubWritableKeys are the only permissions that may be "write": what an
// agent needs to push a branch and open a PR or issue. Everything else stays
// read, so a leaked token can never touch workflows, checks or settings.
var githubWritableKeys = []string{"contents", "pull_requests", "issues"}

// GitHubWritablePermissions lists the permission keys that may be "write". The
// result is a fresh slice the caller may keep.
func GitHubWritablePermissions() []string { return slices.Clone(githubWritableKeys) }

// GitHubPermissionAllowed reports whether a binding may ask for key at level:
// "read" on any known key, "write" only on a writable one. Config validation
// and the minter both call it, so the rule has one definition.
func GitHubPermissionAllowed(key, level string) bool {
	if !slices.Contains(githubPermissionKeys, key) {
		return false
	}
	return level == "read" || (level == "write" && slices.Contains(githubWritableKeys, key))
}

// HasPAT reports whether any owner takes its credential from a static token.
func (b GitHubBinding) HasPAT() bool {
	for _, o := range b.Owners {
		if o.Token != "" {
			return true
		}
	}
	return false
}

// PushRoute reports whether the binding can push to some repository, which is
// when a box needs its push URL routed to GitHub: a PAT owner, or an App
// binding granted contents write.
func (b GitHubBinding) PushRoute() bool {
	return b.HasPAT() || b.EffectivePermissions()["contents"] == "write"
}

// CanPush reports whether the binding can push to owner/repo: the repository is
// bound, and the credential is a PAT or the App token holds contents write.
func (b GitHubBinding) CanPush(owner, repo string) bool {
	o, ok := b.OwnerEntry(owner)
	if !ok || !slices.ContainsFunc(o.Repos, func(r string) bool { return strings.EqualFold(r, repo) }) {
		return false
	}
	return o.Token != "" || b.EffectivePermissions()["contents"] == "write"
}

// DefaultGitHubPermissions is what an empty Permissions means: read access to
// code, issues, PRs and CI results.
func DefaultGitHubPermissions() map[string]string {
	return map[string]string{
		"actions": "read", "checks": "read", "contents": "read",
		"pull_requests": "read", "issues": "read", "metadata": "read",
	}
}

// EffectivePermissions is Permissions, or the defaults when none are set. The
// result is a fresh map the caller may keep.
func (b GitHubBinding) EffectivePermissions() map[string]string {
	if len(b.Permissions) == 0 {
		return DefaultGitHubPermissions()
	}
	return maps.Clone(b.Permissions)
}

// OwnerEntry finds the entry for an owner; GitHub logins are case-insensitive.
func (b GitHubBinding) OwnerEntry(owner string) (GitHubOwner, bool) {
	for _, o := range b.Owners {
		if strings.EqualFold(o.Owner, owner) {
			return o, true
		}
	}
	return GitHubOwner{}, false
}

// ValidGitHubOwner reports whether s is a legal GitHub login.
func ValidGitHubOwner(s string) bool { return githubOwnerRe.MatchString(s) }

// ValidGitHubRepo reports whether s is a legal repository name.
func ValidGitHubRepo(s string) bool {
	return s != "." && s != ".." && githubRepoRe.MatchString(s)
}

// Validate refuses a binding that could not be used safely: every permission
// is read, or write on a writable key, and every owner, repo and host is well
// formed.
func (b GitHubBinding) Validate() error {
	if len(b.Owners) == 0 {
		return errors.New("github.owners needs at least one owner")
	}
	seen := map[string]bool{}
	for i, o := range b.Owners {
		if !ValidGitHubOwner(o.Owner) {
			return fmt.Errorf("github.owners[%d].owner %q is not a GitHub login (letters, digits, -; at most 39 characters)", i, o.Owner)
		}
		if k := strings.ToLower(o.Owner); seen[k] {
			return fmt.Errorf("github.owners[%d].owner %q is listed twice (logins are case-insensitive)", i, o.Owner)
		} else {
			seen[k] = true
		}
		if hasApp, hasTok := strings.TrimSpace(o.App) != "", o.Token != ""; hasApp == hasTok {
			return fmt.Errorf("github.owners[%d]: set exactly one of app or token", i)
		}
		if o.Token != "" && !tokenNameRe.MatchString(o.Token) {
			return fmt.Errorf("github.owners[%d].token %q is not an entry name", i, o.Token)
		}
		if len(o.Repos) == 0 {
			return fmt.Errorf("github.owners[%d].repos needs at least one repository", i)
		}
		if len(o.Repos) > maxGitHubRepos {
			return fmt.Errorf("github.owners[%d].repos has more than %d entries", i, maxGitHubRepos)
		}
		repos := map[string]bool{}
		for j, r := range o.Repos {
			if !ValidGitHubRepo(r) {
				return fmt.Errorf("github.owners[%d].repos[%d] %q is not a repository name (letters, digits, . _ -)", i, j, r)
			}
			if repos[r] {
				return fmt.Errorf("github.owners[%d].repos[%d] %q is listed twice", i, j, r)
			}
			repos[r] = true
		}
	}
	// Non-nil but empty is refused: EffectivePermissions would read it as the
	// defaults, so a client that sent "none ticked" would silently get six reads.
	if b.Permissions != nil && len(b.Permissions) == 0 {
		return errors.New("github.permissions: empty; omit it for the defaults or tick at least one")
	}
	for _, k := range slices.Sorted(maps.Keys(b.Permissions)) {
		if !slices.Contains(githubPermissionKeys, k) {
			return fmt.Errorf("github.permissions: %q is not an allowed permission (want one of %s)", k, strings.Join(githubPermissionKeys, ", "))
		}
		if v := b.Permissions[k]; !GitHubPermissionAllowed(k, v) {
			return fmt.Errorf("github.permissions: %q cannot be %q: only %s may be \"write\", every other permission is \"read\"", k, v, strings.Join(githubWritableKeys, ", "))
		}
	}
	if len(b.EgressAllow) > maxEgressAllow {
		return fmt.Errorf("github.egress_allow has more than %d entries", maxEgressAllow)
	}
	for i, p := range b.EgressAllow {
		if err := hostmatch.ValidPattern(p); err != nil {
			return fmt.Errorf("github.egress_allow[%d] %q: %v", i, p, err)
		}
	}
	return nil
}

// Hash is 12 hex characters over a normalised copy of the binding: owners,
// repos and egress hosts compare as sets and an empty Permissions equals the
// defaults, so none of those orderings or spellings changes the box key.
func (b GitHubBinding) Hash() string {
	c := GitHubBinding{Permissions: b.EffectivePermissions()}
	c.Owners = make([]GitHubOwner, len(b.Owners))
	for i, o := range b.Owners {
		o.Repos = slices.Clone(o.Repos)
		slices.Sort(o.Repos)
		c.Owners[i] = o
	}
	slices.SortFunc(c.Owners, func(x, y GitHubOwner) int {
		return strings.Compare(strings.ToLower(x.Owner), strings.ToLower(y.Owner))
	})
	if len(b.EgressAllow) > 0 {
		c.EgressAllow = slices.Clone(b.EgressAllow)
		slices.Sort(c.EgressAllow)
	}
	j, _ := json.Marshal(c)
	sum := sha256.Sum256(j)
	return hex.EncodeToString(sum[:6])
}

// Clone is a deep copy: a session keeps the binding it was spawned with, and a
// later profile edit must never reach it through a shared slice or map.
func (b GitHubBinding) Clone() *GitHubBinding {
	c := GitHubBinding{EgressAllow: slices.Clone(b.EgressAllow)}
	if b.Permissions != nil {
		c.Permissions = maps.Clone(b.Permissions)
	}
	if b.Owners != nil {
		c.Owners = make([]GitHubOwner, len(b.Owners))
		for i, o := range b.Owners {
			o.Repos = slices.Clone(o.Repos)
			c.Owners[i] = o
		}
	}
	return &c
}

// ValidateGitHubApps checks the instance's github_apps list: unique names, a
// usable client id and a private key that is an absolute file path or a store
// value.
func ValidateGitHubApps(apps []GitHubApp) error {
	seen := map[string]bool{}
	for i, a := range apps {
		if !profileNameRe.MatchString(a.Name) {
			return fmt.Errorf("github_apps[%d].name %q must be letters, digits, . _ - (at most 64)", i, a.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("github_apps[%d].name %q is a duplicate", i, a.Name)
		}
		seen[a.Name] = true
		if !githubClientIDRe.MatchString(a.ClientID) {
			return fmt.Errorf("github_apps[%d].client_id %q must be the App's client id (letters, digits, . _ -)", i, a.ClientID)
		}
		switch a.PrivateKey.Type {
		case "file":
			if !filepath.IsAbs(a.PrivateKey.Path) {
				return fmt.Errorf("github_apps[%d].private_key.path %q must be an absolute path", i, a.PrivateKey.Path)
			}
			if a.PrivateKey.Var != "" || a.PrivateKey.ID != "" {
				return fmt.Errorf("github_apps[%d].private_key: a \"file\" reference sets only path", i)
			}
		case "store":
			if err := a.PrivateKey.Validate(); err != nil {
				return fmt.Errorf("github_apps[%d].private_key: %w", i, err)
			}
		default:
			return fmt.Errorf("github_apps[%d].private_key.type %q is not supported (want \"file\" or \"store\")", i, a.PrivateKey.Type)
		}
	}
	return nil
}

// CheckGitHubApps refuses a binding whose owners name an App the instance does
// not define. An owner that names a token entry has no App to check.
func CheckGitHubApps(a AgentConfig, apps []GitHubApp) error {
	if a.GitHub == nil {
		return nil
	}
	for i, o := range a.GitHub.Owners {
		if o.App == "" && o.Token != "" {
			continue
		}
		if !slices.ContainsFunc(apps, func(x GitHubApp) bool { return x.Name == o.App }) {
			return undefinedRef(RefGitHubApp, o.App, "github.owners[%d].app %q is not defined in github_apps", i, o.App)
		}
	}
	return nil
}

// validateGitHub is the one check for an agent's GitHub binding, shared by
// ValidateProfile and Config.Validate. Like validateAWS, the runtime must be
// named "box": an empty runtime means "the instance default", which may be host.
func validateGitHub(a AgentConfig) error {
	if a.GitHub == nil {
		return nil
	}
	if a.Runtime != RuntimeBox {
		return errors.New("github binding requires runtime: box (set it explicitly); the host tier cannot contain credentials")
	}
	return a.GitHub.Validate()
}

// Bindings is the set of credential bindings a session carries.
type Bindings struct {
	AWS *AWSBinding
	// AWSConns are clones of the host aws_connections a profile names, sorted by
	// name. Exclusive with AWS: a session binds one or the other.
	AWSConns []AWSConnection
	GitHub   *GitHubBinding
	Tokens   []StaticToken // clones of the entries named in a profile's tokens
	PATs     []StaticToken // clones of the entries GitHub owners name in token (deduped)
	// EgressAllow and EgressOpen are the profile's egress policy. They are part
	// of the scope hash (two policies never share a box) but not of Any().
	EgressAllow []string
	EgressOpen  bool
}

// validateEgressPolicy checks a profile's egress_allow and egress_open.
func validateEgressPolicy(a AgentConfig) error {
	if len(a.EgressAllow) == 0 && !a.EgressOpen {
		return nil
	}
	if a.Runtime != RuntimeBox {
		return errors.New("egress_allow and egress_open need runtime: box (set it explicitly)")
	}
	if len(a.EgressAllow) > maxEgressAllow {
		return fmt.Errorf("egress_allow has more than %d entries", maxEgressAllow)
	}
	for i, p := range a.EgressAllow {
		if err := hostmatch.ValidPattern(p); err != nil {
			return fmt.Errorf("egress_allow[%d] %q: %v", i, p, err)
		}
	}
	return nil
}

// Any reports whether the session has at least one binding. PATs do not count
// on their own: a PAT owner is part of the GitHub binding.
func (b Bindings) Any() bool {
	return b.AWS != nil || len(b.AWSConns) > 0 || b.GitHub != nil || len(b.Tokens) > 0
}

// Hash identifies the whole scope. With no GitHub binding it is exactly the AWS
// binding's hash, so a session that existed before GitHub bindings keeps its box
// key and binding directory. With neither binding it is "". Named AWS
// connections fold in after the GitHub binding, only when there are some.
// Token entries are
// folded in only when there are some, so a session that binds none keeps the
// hash it had before they existed.
func (b Bindings) Hash() string {
	aws := ""
	if b.AWS != nil {
		aws = b.AWS.Hash()
	}
	base := aws
	if b.GitHub != nil {
		sum := sha256.Sum256([]byte(aws + "\x00" + b.GitHub.Hash()))
		base = hex.EncodeToString(sum[:6])
	}
	if len(b.AWSConns) > 0 {
		sum := sha256.Sum256([]byte(base + "\x00awsconns\x00" + AWSConnsHash(b.AWSConns)))
		base = hex.EncodeToString(sum[:6])
	}
	if len(b.Tokens)+len(b.PATs) > 0 {
		sum := sha256.Sum256([]byte(base + "\x00tokens\x00" + TokensHash(b.Tokens, b.PATs)))
		base = hex.EncodeToString(sum[:6])
	}
	if len(b.EgressAllow) > 0 || b.EgressOpen {
		allow := slices.Clone(b.EgressAllow)
		slices.Sort(allow)
		sum := sha256.Sum256([]byte(base + "\x00egress\x00" + strings.Join(allow, ",") + "\x00open=" + strconv.FormatBool(b.EgressOpen)))
		base = hex.EncodeToString(sum[:6])
	}
	return base
}

// ParseGitHubRepo reads owner and repo out of a remote URL. It accepts only the
// host github.com (any case), in three spellings: https://github.com/o/r,
// git@github.com:o/r and ssh://git@github.com/o/r, each with an optional ".git"
// and, for the URL forms, a trailing slash. A URL that carries credentials, a
// port, a query, a fragment or any further path segment is refused rather than
// cleaned: the answer feeds GH_REPO, so a remote that is not plainly a GitHub
// repository must yield nothing.
func ParseGitHubRepo(raw string) (owner, repo string, ok bool) {
	var path string
	switch {
	case strings.HasPrefix(raw, "git@"):
		// scp-like: git@github.com:owner/repo
		host, rest, found := strings.Cut(raw[len("git@"):], ":")
		if !found || !strings.EqualFold(host, "github.com") {
			return "", "", false
		}
		path = rest
	case strings.HasPrefix(raw, "https://"), strings.HasPrefix(raw, "ssh://"):
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") || u.Port() != "" ||
			u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return "", "", false
		}
		switch u.Scheme {
		case "https":
			if u.User != nil {
				return "", "", false
			}
		case "ssh":
			if _, hasPw := u.User.Password(); u.User == nil || u.User.Username() != "git" || hasPw {
				return "", "", false
			}
		}
		if !strings.HasPrefix(u.Path, "/") {
			return "", "", false
		}
		path = u.Path[1:]
	default:
		return "", "", false
	}
	path = strings.TrimSuffix(path, "/")
	owner, repo, found := strings.Cut(path, "/")
	if !found || strings.Contains(repo, "/") {
		return "", "", false
	}
	repo = strings.TrimSuffix(repo, ".git")
	if !ValidGitHubOwner(owner) || !ValidGitHubRepo(repo) {
		return "", "", false
	}
	return owner, repo, true
}
