package broker

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/awsscope"
	"github.com/culiops/culibox/internal/config"
)

//go:embed agentbox-cred.js
var clientScript []byte

// ghScript is the in-box gh wrapper: it asks the broker for an installation
// token and runs the real gh with it (the shim at /usr/local/bin/gh in the
// image calls it). Only a binding with a GitHub scope gets it.
//
//go:embed agentbox-gh.js
var ghScript []byte

// envScript is the in-box env wrapper: it fetches the named static tokens from
// the broker and runs one command with them (agentbox-env NAME... -- CMD).
// Only a binding with static tokens gets it, with TokensFile beside it.
//
//go:embed agentbox-env.js
var envScript []byte

// TokensFile is the non-secret listing of a binding's tokens, in the binding
// dir (so /opt/agentbox/tokens.json in the box); `agentbox-env --list` reads it.
const TokensFile = "tokens.json"

// AWSListFile is the non-secret listing of a session's AWS connections, in the
// binding dir (so /opt/agentbox/aws.json in the box); `agentbox-env --list`
// reads it.
const AWSListFile = "aws.json"

// GitHubListFile is the non-secret listing of a session's GitHub binding, in the
// binding dir, for `agentbox-env --list`.
const GitHubListFile = "github.json"

// GHConfigDir is gh's config dir in a bound box: a path that dies with the box,
// so nothing gh writes there (hosts.yml, caches) outlives the session.
const GHConfigDir = "/tmp/agentbox-gh"

// BoxDir is where a binding dir is mounted (read-only) inside a box.
const BoxDir = "/opt/agentbox"

// StaticCredEnv are variables through which an SDK would take credentials
// ahead of credential_process. A bound session must never carry them, or the
// broker is silently bypassed.
var StaticCredEnv = []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AWS_SECURITY_TOKEN", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
	"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME", "AWS_SHARED_CREDENTIALS_FILE"}

// TokenEnv carries a session's broker token into its box. The session layer
// sets it per spawn; BoxEnv, which is per binding, never does.
const TokenEnv = "AGENTBOX_BROKER_TOKEN"

// StripEnv is every variable a bound session must not inherit from its
// profile or the daemon: the static-credential routes around credential_process
// (StaticCredEnv), a profile selector that would pick something other than the
// broker's profile, and every name the broker itself sets (BoxEnv and
// TokenEnv), so a profile can never shadow or duplicate them. It is the single
// list the session layer strips before adding the broker's values.
func StripEnv() []string {
	return append(awsStripEnv(), "AGENTBOX_BROKER", TokenEnv)
}

// awsStripEnv is the AWS family of StripEnv: the static-credential routes, the
// profile and region selectors, the config file and the Terraform plan flags a
// bound session must not inherit. A Host session that takes AWS from this
// machine's login keeps all of them (#375).
func awsStripEnv() []string {
	return append(slices.Clone(StaticCredEnv),
		"AWS_CONFIG_FILE", "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
		"TF_CLI_ARGS_plan")
}

// StripGitHubEnv is what a session with a GitHub binding must also not inherit,
// on top of StripEnv: every static-token route around the broker, plus every
// name GitHubEnv sets (a profile must not repoint gh, git or the owner list).
// It is kept apart from StripEnv so an AWS-only session keeps whatever GitHub
// variables its profile sets.
func StripGitHubEnv() []string {
	return []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
		"GH_CONFIG_DIR", "GH_REPO", "GH_HOST",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1",
		// Index 2 is the clone push rewrite (session.withGitPushRewrite) and must
		// never be a profile's own entry.
		"GIT_CONFIG_KEY_2", "GIT_CONFIG_VALUE_2",
		"GIT_TERMINAL_PROMPT", "GH_PROMPT_DISABLED", "AGENTBOX_GH_OWNERS",
		// A profile must not add git config through the env (credential.helper=store
		// would write minted tokens to disk) or a prompt helper that could capture one.
		"GIT_CONFIG_PARAMETERS", "GIT_ASKPASS", "SSH_ASKPASS"}
}

// EnsureBindingDir writes <stateRoot>/broker/binding-<hash>/ with the files of
// bindingFiles (PAT entries go through the gh/git helpers and do not count as
// static tokens). The bindings
// and each token entry are validated first (a hand-edited profiles.json
// is not re-validated at load, and Region is formatted into an INI file). With
// only an AWS binding the directory and its contents are what they always were.
// All writes go through os.Root, so a symlink planted at any level is refused
// rather than followed (repo rule: host writes go through os.Root).
func EnsureBindingDir(stateRoot string, bs config.Bindings) (string, error) {
	return EnsureBindingDirWith(stateRoot, bs, nil)
}

// EnsureBindingDirWith is EnsureBindingDir with the spawn's base-role probe
// results, keyed by binding key (the connection name), which aws.json carries
// per connection. A connection missing from base reads as "not checked".
func EnsureBindingDirWith(stateRoot string, bs config.Bindings, base map[string]awsprobe.Result) (string, error) {
	root, rel, err := openBindingDir(stateRoot, bs)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for _, f := range bindingFiles(bs, base, "node "+BoxDir+"/agentbox-cred.js") {
		if err := replaceFile(root, rel, f.name, f.data); err != nil {
			return "", fmt.Errorf("broker: writing %s: %w", f.name, err)
		}
	}
	return filepath.Join(stateRoot, rel), nil
}

// bindingFile is one file of a binding's script dir.
type bindingFile struct {
	name string
	data []byte
}

// bindingFiles is the file set of a binding's script dir, shared by a box's
// binding dir and a Host session's own dir. cred is the command that runs
// agentbox-cred.js as aws-config's credential_process names it: the box's
// fixed path, or a Host dir's single-quoted one. aws-config when there is an
// AWS binding (or named AWS connections, which also get aws.json and
// agentbox-env.js), agentbox-cred.js always, agentbox-gh.js, github.json and
// agentbox-env.js when there is a GitHub binding, agentbox-env.js and
// tokens.json when there are static tokens.
func bindingFiles(bs config.Bindings, base map[string]awsprobe.Result, cred string) []bindingFile {
	files := []bindingFile{{"agentbox-cred.js", clientScript}}
	if bs.AWS != nil {
		cfg := fmt.Sprintf("[profile agentbox]\ncredential_process = %s\nregion = %s\n", cred, bs.AWS.Region)
		files = append(files, bindingFile{"aws-config", []byte(cfg)})
	}
	if len(bs.AWSConns) > 0 {
		files = append(files, bindingFile{"aws-config", awsConfigFor(bs.AWSConns, cred)},
			bindingFile{AWSListFile, AWSListing(bs.AWSConns, base)})
	}
	if bs.GitHub != nil {
		files = append(files, bindingFile{"agentbox-gh.js", ghScript}, bindingFile{GitHubListFile, GitHubListing(*bs.GitHub)})
	}
	// The image's agentbox-env shim requires the script, so connections and a
	// GitHub binding get it too (for --list); tokens.json only exists with tokens.
	if hasEnvScript(bs) {
		files = append(files, bindingFile{"agentbox-env.js", envScript})
	}
	if len(bs.Tokens) > 0 {
		files = append(files, bindingFile{TokensFile, TokensListing(bs.Tokens)})
	}
	return files
}

// hasEnvScript: the binding's script dir holds agentbox-env.js.
func hasEnvScript(bs config.Bindings) bool {
	return len(bs.Tokens) > 0 || len(bs.AWSConns) > 0 || bs.GitHub != nil
}

// openBindingDir validates the bindings, then opens the state root and makes
// sure <stateRoot>/broker/binding-<hash> exists as a plain directory. It writes
// no file into it. The caller closes the returned root.
func openBindingDir(stateRoot string, bs config.Bindings) (*os.Root, string, error) {
	if err := validateBindings(bs); err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, "", fmt.Errorf("broker: opening state root: %w", err)
	}
	rel := filepath.Join("broker", "binding-"+bs.Hash())
	for _, d := range []string{"broker", rel} {
		if err := root.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			root.Close()
			return nil, "", fmt.Errorf("broker: %w", err)
		}
	}
	if fi, err := root.Lstat(rel); err != nil || !fi.IsDir() {
		root.Close()
		return nil, "", fmt.Errorf("broker: %s is not a plain directory", rel)
	}
	return root, rel, nil
}

func validateBindings(bs config.Bindings) error {
	if !bs.Any() {
		return errors.New("broker: no binding to prepare")
	}
	if bs.AWS != nil {
		if err := bs.AWS.Validate(); err != nil {
			return fmt.Errorf("broker: %w", err)
		}
	}
	if bs.GitHub != nil {
		if err := bs.GitHub.Validate(); err != nil {
			return fmt.Errorf("broker: %w", err)
		}
	}
	for _, t := range bs.Tokens {
		if err := t.Validate(); err != nil {
			return fmt.Errorf("broker: %w", err)
		}
	}
	// Connection names and regions are formatted into an INI file: re-check
	// each one here, as for the inline binding (a hand-edited config.json is not
	// re-validated at load).
	for i, c := range bs.AWSConns {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("broker: aws_connections[%d]: %w", i, err)
		}
	}
	return nil
}

// WriteAWSListing rewrites aws.json (and the box's agentbox-env.js, which may
// predate the probe) in an existing binding dir, with new
// base-role probe results (a re-probe that landed after the spawn). It is a no-op
// when the bindings have no named connections (the legacy binding has no
// aws.json). It never creates the dir: a missing one, or one that is not a plain
// directory, is an error. The write goes through os.Root and replaceFile.
func WriteAWSListing(stateRoot string, bs config.Bindings, base map[string]awsprobe.Result) error {
	if len(bs.AWSConns) == 0 {
		return nil
	}
	if err := validateBindings(bs); err != nil {
		return err
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return fmt.Errorf("broker: opening state root: %w", err)
	}
	defer root.Close()
	return writeAWSListingIn(root, filepath.Join("broker", "binding-"+bs.Hash()), bs, base)
}

// writeAWSListingIn rewrites aws.json and agentbox-env.js in rel, an existing
// plain directory of root (a binding dir or a Host session's dir).
func writeAWSListingIn(root *os.Root, rel string, bs config.Bindings, base map[string]awsprobe.Result) error {
	if fi, err := root.Lstat(rel); err != nil || !fi.IsDir() {
		return fmt.Errorf("broker: %s is not a plain directory", rel)
	}
	// The env script goes too: a box spawned before the base-role probe has an
	// old one, and the re-probe would otherwise leave its --list output stale.
	for _, f := range []bindingFile{{"agentbox-env.js", envScript}, {AWSListFile, AWSListing(bs.AWSConns, base)}} {
		if err := replaceFile(root, rel, f.name, f.data); err != nil {
			return fmt.Errorf("broker: writing %s: %w", f.name, err)
		}
	}
	return nil
}

// AWSConfigFor is the aws-config of a session with named connections: a
// [default] profile that only prints guidance (no silent default), then one
// profile per connection, sorted by name, each asking the broker for that
// connection. Names and regions are validated by the caller (EnsureBindingDir).
func AWSConfigFor(conns []config.AWSConnection) []byte {
	return awsConfigFor(conns, "node "+BoxDir+"/agentbox-cred.js")
}

// awsConfigFor is AWSConfigFor with cred, the command that runs
// agentbox-cred.js (already shell-quoted where it has to be).
func awsConfigFor(conns []config.AWSConnection, cred string) []byte {
	sorted := slices.Clone(conns)
	slices.SortFunc(sorted, func(a, b config.AWSConnection) int { return strings.Compare(a.Name, b.Name) })
	var sb strings.Builder
	fmt.Fprintf(&sb, "[default]\ncredential_process = %s aws\n", cred)
	for _, c := range sorted {
		fmt.Fprintf(&sb, "\n[profile %s]\ncredential_process = %s aws %s\nregion = %s\n", c.Name, cred, c.Name, c.Region)
	}
	return []byte(sb.String())
}

// awsListEntry is one row of aws.json: what `agentbox-env --list` shows and
// nothing more. There is deliberately no field for the source profile, the
// elevate role, s3_read or egress.
type awsListEntry struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Prod      bool   `json:"prod"`
	AccountID string `json:"account_id"`
	RoleARN   string `json:"role_arn"`
	Region    string `json:"region"`
	// Mode is "sso_direct" for a connection that uses the SSO role directly
	// (it has no role_arn); absent for an assumed agent role.
	Mode         string `json:"mode,omitempty"`
	WriteCapable bool   `json:"write_capable"`
	Description  string `json:"description,omitempty"`
	awsprobe.Fields
}

// AWSListing is the JSON the box shows for the AWS section of `agentbox-env
// --list`, one row per connection sorted by name. base holds the base-role probe
// results keyed by connection name; a connection missing from it is "not checked",
// never read-only.
func AWSListing(conns []config.AWSConnection, base map[string]awsprobe.Result) []byte {
	sorted := slices.Clone(conns)
	slices.SortFunc(sorted, func(a, b config.AWSConnection) int { return strings.Compare(a.Name, b.Name) })
	out := make([]awsListEntry, len(sorted))
	for i, c := range sorted {
		out[i] = awsListEntry{Name: c.Name, Label: c.Label, Prod: c.Prod, AccountID: awsscope.AccountOf(c.RoleARN),
			RoleARN: c.RoleARN, Region: c.Region, WriteCapable: c.Elevate != nil, Description: c.Description,
			Fields: awsprobe.Lookup(base, c.Name).Fields()}
		if c.SSODirect() {
			// No role ARN to read the account from: it is the profile's
			// sso_account_id, from the host's files (no network). Unreadable is
			// shown as no account, which fails nothing.
			out[i].Mode = config.AWSModeSSODirect
			out[i].AccountID, _ = SSOAccountOf(context.Background(), c.SourceProfile)
		}
	}
	b, err := json.Marshal(out)
	if err != nil { // plain strings and bools: cannot happen
		return []byte("[]\n")
	}
	return append(b, '\n')
}

// tokenListEntry is one row of tokens.json: everything `agentbox-env --list`
// shows and nothing secret. There is deliberately no field for the value
// reference (env var name or file path).
type tokenListEntry struct {
	Name        string            `json:"name"`
	Provider    string            `json:"provider,omitempty"`
	Label       string            `json:"label,omitempty"`
	Prod        bool              `json:"prod,omitempty"`
	Description string            `json:"description,omitempty"`
	Env         []string          `json:"env"`
	ConfigEnv   map[string]string `json:"config_env,omitempty"`
	Egress      []tokenListHost   `json:"egress"`
	MCPServer   string            `json:"mcp_server,omitempty"`
}

type tokenListHost struct {
	Host    string `json:"host"`
	Port    int    `json:"port,omitempty"` // only when not 443
	Private bool   `json:"private,omitempty"`
}

// gitHubListRow is one owner of a GitHub binding as `agentbox-env --list`
// shows it. Via says how the broker mints for it ("app" or "token"), never the
// App's name or the PAT entry's name. The permissions are the binding's, the
// same on every App row; a token row has none, because CuliBox does not narrow
// a personal access token (the token's own scopes apply).
type gitHubListRow struct {
	Owner       string            `json:"owner"`
	Repos       []string          `json:"repos"`
	Via         string            `json:"via"`
	Permissions map[string]string `json:"permissions,omitempty"`
}

// GitHubListing is the JSON the box shows for the GitHub part of `agentbox-env
// --list`: per owner its repos, how it is minted and the effective permissions.
func GitHubListing(g config.GitHubBinding) []byte {
	perms := g.EffectivePermissions()
	out := make([]gitHubListRow, len(g.Owners))
	for i, o := range g.Owners {
		out[i] = gitHubListRow{Owner: o.Owner, Repos: slices.Clone(o.Repos), Via: "app", Permissions: perms}
		if o.Token != "" {
			out[i].Via, out[i].Permissions = "token", nil
		}
	}
	b, err := json.Marshal(out)
	if err != nil { // plain strings: cannot happen
		return []byte("[]\n")
	}
	return append(b, '\n')
}

// TokensListing is the JSON the box shows for `agentbox-env --list`: per entry
// its name, labels, the variable names it sets, the config values (not secret
// by definition), the hosts it may reach and its MCP server name. Never the
// value, the variable or file that holds it, or a cidr.
func TokensListing(ts []config.StaticToken) []byte {
	out := make([]tokenListEntry, len(ts))
	for i, t := range ts {
		e := tokenListEntry{Name: t.Name, Provider: t.Provider, Label: t.Label, Prod: t.Prod, Description: t.Description,
			Env: slices.Clone(t.Env), ConfigEnv: t.ConfigEnv, Egress: make([]tokenListHost, len(t.Egress))}
		if e.Env == nil {
			e.Env = []string{}
		}
		for j, h := range t.Egress {
			e.Egress[j] = tokenListHost{Host: h.Host, Private: h.Private}
			if p := h.EffectivePort(); p != 443 {
				e.Egress[j].Port = p
			}
		}
		if t.MCP != nil {
			e.MCPServer = t.MCP.Server
		}
		out[i] = e
	}
	b, err := json.Marshal(out)
	if err != nil { // plain strings, ints and bools: cannot happen
		return []byte("[]\n")
	}
	return append(b, '\n')
}

var sessionKeyRe = regexp.MustCompile(`^[0-9a-z-]{1,40}$`)

// WriteSessionMCP writes one session's MCP config file into the binding dir
// (ensuring the dir first, by the same path as a spawn) and returns where the
// box sees it. The key names the session and must be plain; the write goes
// through os.Root, so a symlink planted at the name is replaced, never followed.
// The file is 0600 and is not secret-bearing: the MCP servers it names run
// under agentbox-env, which fetches their tokens.
func WriteSessionMCP(stateRoot string, bs config.Bindings, key string, doc []byte) (string, error) {
	if !sessionKeyRe.MatchString(key) {
		return "", fmt.Errorf("broker: bad session key %q", key)
	}
	// Only the directory is ensured: the binding's files (aws.json among them,
	// which may hold a later re-probe result) were written by the spawn.
	root, rel, err := openBindingDir(stateRoot, bs)
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := "mcp-" + key + ".json"
	if err := replaceFile(root, rel, name, doc); err != nil {
		return "", fmt.Errorf("broker: writing %s: %w", name, err)
	}
	return BoxDir + "/" + name, nil
}

// replaceFile atomically replaces dir/name inside root. The temp name is unique
// per call: concurrent spawns on one binding run this at the same time, and a
// shared temp name let one writer truncate another's file or rename it away
// first (ENOENT, a failed spawn). O_EXCL refuses anything already at the temp
// path, a planted symlink included; the temp file is removed on any failure.
func replaceFile(root *os.Root, dir, name string, data []byte) error {
	return replaceFileMode(root, dir, name, data, 0o600)
}

// replaceFileMode is replaceFile for a file of mode perm (a Host session's
// shims are 0700); a mode other than 0600 is set explicitly, whatever the umask.
func replaceFileMode(root *os.Root, dir, name string, data []byte, perm os.FileMode) error {
	tmp := filepath.Join(dir, "."+name+"."+NewNonce()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil && perm != 0o600 {
		err = f.Chmod(perm)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, filepath.Join(dir, name))
	}
	if err != nil {
		_ = root.Remove(tmp)
	}
	return err
}

// BoxEnv is the environment that points a box's tooling at the broker: the AWS
// block when there is an AWS binding (unchanged), the broker endpoint always,
// and GitHubEnv when there is a GitHub binding. The per-session token is added
// by the session layer, not here.
func BoxEnv(bs config.Bindings, boxURL string) []string {
	env := awsEnv(bs, BoxDir+"/aws-config")
	env = append(env, "AGENTBOX_BROKER="+boxURL)
	if bs.AWS != nil || len(bs.AWSConns) > 0 {
		env = append(env, tfPlanNoLock)
	}
	if bs.GitHub != nil {
		env = append(env, GitHubEnv(*bs.GitHub)...)
	}
	return env
}

// tfPlanNoLock: plan never takes the state lock, M3a is strictly read-only.
const tfPlanNoLock = "TF_CLI_ARGS_plan=-lock=false"

// awsEnv is the AWS block of a bound session's env; configFile is the path of
// its aws-config as the session sees it.
func awsEnv(bs config.Bindings, configFile string) []string {
	var env []string
	if bs.AWS != nil {
		env = append(env,
			"AWS_CONFIG_FILE="+configFile,
			"AWS_PROFILE=agentbox",
			"AWS_REGION="+bs.AWS.Region,
			"AWS_DEFAULT_REGION="+bs.AWS.Region)
	}
	if len(bs.AWSConns) > 0 {
		// A profile selector only when there is nothing to choose; a region only
		// when every connection shares it (an AWS_REGION would override each
		// profile's own region). Otherwise the SDK takes both from the profile.
		env = append(env, "AWS_CONFIG_FILE="+configFile)
		if len(bs.AWSConns) == 1 {
			env = append(env, "AWS_PROFILE="+bs.AWSConns[0].Name)
		}
		if r := config.AWSConnectionsRegion(bs.AWSConns); r != "" {
			env = append(env, "AWS_REGION="+r, "AWS_DEFAULT_REGION="+r)
		}
	}
	return env
}

// GitHubEnv wires git and gh to the broker. git asks agentbox-cred.js for a
// token per request (useHttpPath makes it say which repo), never prompts, and
// stores nothing; gh keeps its config in a throwaway dir and never prompts. No
// token is in this env: both fetch one over the broker socket when they need it.
func GitHubEnv(g config.GitHubBinding) []string {
	return gitHubEnv(g, "node "+BoxDir+"/agentbox-cred.js", GHConfigDir, nil, nil)
}

// gitHubEnv is GitHubEnv with cred the command that runs agentbox-cred.js,
// ghDir gh's config dir, and extra git config pairs before (pre) and after
// (post) the helper's two.
func gitHubEnv(g config.GitHubBinding, cred, ghDir string, pre, post [][2]string) []string {
	owners := make([]string, len(g.Owners))
	for i, o := range g.Owners {
		owners[i] = o.Owner
	}
	pairs := slices.Concat(pre, [][2]string{
		{"credential.https://github.com.helper", "!" + cred + " github-git"},
		{"credential.https://github.com.useHttpPath", "true"},
	}, post)
	env := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(pairs))}
	for i, kv := range pairs {
		n := strconv.Itoa(i)
		env = append(env, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
	}
	return append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GH_CONFIG_DIR="+ghDir,
		"GH_PROMPT_DISABLED=1",
		"AGENTBOX_GH_OWNERS="+strings.Join(owners, ","))
}
