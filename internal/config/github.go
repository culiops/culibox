package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/culiops/agent-box/internal/hostmatch"
)

// KeyRef says where a secret's bytes live. N1 knows only "file": an absolute
// path on the host that the broker reads. The bytes never enter a box.
type KeyRef struct {
	Type string `json:"type"` // "file" only in N1
	Path string `json:"path,omitempty"`
}

// GitHubApp is a named GitHub App connection, declared once in config.json
// (github_apps) and referenced by name from a profile's binding.
type GitHubApp struct {
	Name       string `json:"name"`
	ClientID   string `json:"client_id"`
	PrivateKey KeyRef `json:"private_key"`
}

// GitHubOwner scopes a binding to named repositories under one account or
// organisation, minted through one App.
type GitHubOwner struct {
	Owner string   `json:"owner"`
	App   string   `json:"app"`
	Repos []string `json:"repos"`
}

// GitHubBinding gives a boxed profile read-only GitHub access through the
// credential broker. Every field shapes the scope, so all of them go into Hash
// and therefore into the box key.
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

// githubPermissionKeys are the only permissions a binding may ask for, and
// every one of them is read-only.
var githubPermissionKeys = []string{"actions", "checks", "contents", "pull_requests", "issues", "metadata", "statuses"}

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
// is read-only and every owner, repo and host is well formed.
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
		if strings.TrimSpace(o.App) == "" {
			return fmt.Errorf("github.owners[%d].app must name a github_apps entry", i)
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
	for _, k := range slices.Sorted(maps.Keys(b.Permissions)) {
		if !slices.Contains(githubPermissionKeys, k) {
			return fmt.Errorf("github.permissions: %q is not an allowed permission (want one of %s)", k, strings.Join(githubPermissionKeys, ", "))
		}
		if v := b.Permissions[k]; v != "read" {
			return fmt.Errorf("github.permissions: %q must be \"read\", got %q: agent-box mints read-only tokens", k, v)
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
// usable client id and a private key that is an absolute file path.
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
		if a.PrivateKey.Type != "file" {
			return fmt.Errorf("github_apps[%d].private_key.type %q is not supported (want \"file\")", i, a.PrivateKey.Type)
		}
		if !filepath.IsAbs(a.PrivateKey.Path) {
			return fmt.Errorf("github_apps[%d].private_key.path %q must be an absolute path", i, a.PrivateKey.Path)
		}
	}
	return nil
}

// CheckGitHubApps refuses a binding whose owners name an App the instance does
// not define.
func CheckGitHubApps(a AgentConfig, apps []GitHubApp) error {
	if a.GitHub == nil {
		return nil
	}
	for i, o := range a.GitHub.Owners {
		if !slices.ContainsFunc(apps, func(x GitHubApp) bool { return x.Name == o.App }) {
			return fmt.Errorf("github.owners[%d].app %q is not defined in github_apps", i, o.App)
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
	AWS    *AWSBinding
	GitHub *GitHubBinding
}

// Any reports whether the session has at least one binding.
func (b Bindings) Any() bool { return b.AWS != nil || b.GitHub != nil }

// Hash identifies the whole scope. With no GitHub binding it is exactly the AWS
// binding's hash, so a session that existed before GitHub bindings keeps its box
// key and binding directory. With neither binding it is "".
func (b Bindings) Hash() string {
	aws := ""
	if b.AWS != nil {
		aws = b.AWS.Hash()
	}
	if b.GitHub == nil {
		return aws
	}
	sum := sha256.Sum256([]byte(aws + "\x00" + b.GitHub.Hash()))
	return hex.EncodeToString(sum[:6])
}
