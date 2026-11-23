package broker

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/config"
)

// A bound Host session runs on the host, so nothing is mounted: its helper
// scripts, shims, listings, aws-config and gh config dir sit in its own
// directory next to its broker socket, <state>/broker/h/<12 hex>/ (dir 0700,
// files 0600, shims and subdirs 0700). CuliBox never writes anywhere else for
// it: only this dir and the session's env (#360 D2). Paths written into
// aws-config, the git helper and the shims are single-quoted, since a data dir
// may hold spaces.

// HostDirEnv names a Host session's own dir in its env; the scripts read their
// listings from it (a box's are at BoxDir).
const HostDirEnv = "AGENTBOX_DIR"

// GHRealEnv names the real gh the gh wrapper runs. Set on Host only, when the
// session PATH has a gh; a box's wrapper runs the image's.
const GHRealEnv = "AGENTBOX_GH_REAL"

// The subdirs of a Host session's dir: the shims (Task 4 puts it first on the
// session PATH) and gh's config dir.
const (
	hostBinDir = "bin"
	hostGHDir  = "gh"
)

// hostShims are the POSIX sh shims in bin/: name → the script it runs.
var hostShims = []struct{ name, script string }{
	{"agentbox-env", "agentbox-env.js"},
	{"gh", "agentbox-gh.js"},
}

// hostFileNames is every file HostFor may write, relative to the dir: a
// respawn of the same session id removes the ones its binding no longer has.
var hostFileNames = []string{"agentbox-cred.js", "agentbox-env.js", "agentbox-gh.js", "aws-config", hostAWSCredsFile,
	AWSListFile, GitHubListFile, TokensFile, hostBinDir + "/agentbox-env", hostBinDir + "/gh"}

// hostAWSCredsFile is a Host session's AWS shared credentials file, always
// empty. The SDKs read the shared credentials file before credential_process,
// so the user's ~/.aws/credentials ([default], or a section named like a
// connection) would silently replace the broker (D3).
const hostAWSCredsFile = "aws-credentials"

// hostGitReset comes before the broker's git helper: an empty helper value
// clears every github.com credential helper git collected so far (the user's
// system and global osxkeychain/store/manager), so only the broker answers.
var hostGitReset = [][2]string{{"credential.https://github.com.helper", ""}}

// hostGitRewrites send a GitHub SSH remote over HTTPS, where the broker's git
// helper answers (D3: the user's own SSH key is not the session's login).
var hostGitRewrites = [][2]string{
	{"url.https://github.com/.insteadOf", "git@github.com:"},
	{"url.https://github.com/.insteadOf", "ssh://git@github.com/"},
}

// hostGitPushIndex is the git config index the clone push rewrite
// (session.withGitPushRewrite) takes after Host's entries; HostStripEnv
// reserves every index up to it, as StripGitHubEnv reserves the box's 2.
const hostGitPushIndex = 5

var hostDirNameRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// shq quotes s for a POSIX shell: in single quotes, each embedded single quote
// closed, backslash-escaped and reopened.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hostRel is the state-root-relative path of a Host session's dir.
func hostRel(sessionID string) string {
	return filepath.Join(filepath.FromSlash(hostsRel), HostDirName(sessionID))
}

// HostFor prepares a bound Host session: it takes a reference on the session's
// own broker socket, writes the session's files next to it and returns the env
// that points the session's AWS, GitHub and token tooling at them, and the dir.
// base is the spawn's base-role probe results (nil without AWS). realGH is the
// absolute path of the real gh on the session PATH ("" when there is none).
// The env holds no token and no PATH (the session layer owns PATH and adds
// TokenEnv). Every call that returns a nil error is paired with one
// ReleaseHost; on error no reference is held.
func (h Hooks) HostFor(sessionID string, bs config.Bindings, base map[string]awsprobe.Result, realGH string) (env []string, dir string, err error) {
	if err := validateBindings(bs); err != nil {
		return nil, "", err
	}
	if realGH != "" && !filepath.IsAbs(realGH) {
		// A bare name would resolve to the gh shim itself.
		return nil, "", fmt.Errorf("broker: the real gh must be an absolute path, not %q", realGH)
	}
	if h.EP.Hosts == nil {
		return nil, "", errors.New("broker: host sockets are not available")
	}
	dir, err = h.EP.Hosts.Acquire(sessionID)
	if err != nil {
		return nil, "", err
	}
	if err := writeHostFiles(h.StateRoot, hostRel(sessionID), dir, bs, base); err != nil {
		h.EP.Hosts.Release(sessionID)
		return nil, "", err
	}
	h.EP.Hosts.setHash(sessionID, bs.Hash())
	return hostEnv(bs, dir, realGH), dir, nil
}

// writeHostFiles writes the binding's files into rel (dir, absolute), the
// session's existing socket dir, through an os.Root at stateRoot: a symlink
// planted at any name is replaced or refused, never followed.
func writeHostFiles(stateRoot, rel, dir string, bs config.Bindings, base map[string]awsprobe.Result) error {
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return fmt.Errorf("broker: opening state root: %w", err)
	}
	defer root.Close()
	if fi, err := root.Lstat(rel); err != nil || !fi.IsDir() {
		return fmt.Errorf("broker: %s is not a plain directory", rel)
	}
	subdirs := []string{hostBinDir}
	if bs.GitHub != nil {
		subdirs = append(subdirs, hostGHDir)
	}
	for _, d := range subdirs {
		p := filepath.Join(rel, d)
		if err := root.Mkdir(p, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("broker: %w", err)
		}
		if fi, err := root.Lstat(p); err != nil || !fi.IsDir() {
			return fmt.Errorf("broker: %s is not a plain directory", p)
		}
		if err := root.Chmod(p, 0o700); err != nil {
			return fmt.Errorf("broker: %w", err)
		}
	}
	written := map[string]bool{}
	files := bindingFiles(bs, base, "node "+shq(dir+"/agentbox-cred.js"))
	if bs.AWS != nil || len(bs.AWSConns) > 0 {
		files = append(files, bindingFile{hostAWSCredsFile, nil}) // empty: see hostAWSCredsFile
	}
	for _, f := range files {
		if err := replaceFile(root, rel, f.name, f.data); err != nil {
			return fmt.Errorf("broker: writing %s: %w", f.name, err)
		}
		written[f.name] = true
	}
	for _, s := range hostShims {
		if !written[s.script] {
			continue
		}
		body := "#!/bin/sh\nexec node " + shq(dir+"/"+s.script) + " \"$@\"\n"
		if err := replaceFileMode(root, filepath.Join(rel, hostBinDir), s.name, []byte(body), 0o700); err != nil {
			return fmt.Errorf("broker: writing %s/%s: %w", hostBinDir, s.name, err)
		}
		written[hostBinDir+"/"+s.name] = true
	}
	for _, name := range hostFileNames {
		if written[name] {
			continue
		}
		if err := root.Remove(filepath.Join(rel, filepath.FromSlash(name))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("broker: removing stale %s: %w", name, err)
		}
	}
	return nil
}

// HostMCPFile is a Host session's own MCP config in its dir (0600, no secret:
// the servers it names run under agentbox-env, which fetches their tokens).
// It is not in hostFileNames on purpose: the session's agent was started with
// --mcp-config pointing here, and the HostFor of a re-attach after a daemon
// restart must not remove what that running agent still reads. It goes with
// the dir.
const HostMCPFile = "mcp.json"

// WriteHostMCP writes a Host session's MCP config into its dir, which HostFor
// made. Like the other files, through an os.Root: a symlink planted at the name
// is replaced, never followed. It returns the file's path.
func (h Hooks) WriteHostMCP(sessionID string, doc []byte) (string, error) {
	root, err := os.OpenRoot(h.StateRoot)
	if err != nil {
		return "", fmt.Errorf("broker: opening state root: %w", err)
	}
	defer root.Close()
	rel := hostRel(sessionID)
	if fi, err := root.Lstat(rel); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("broker: %s is not a plain directory", rel)
	}
	if err := replaceFile(root, rel, HostMCPFile, doc); err != nil {
		return "", fmt.Errorf("broker: writing %s: %w", HostMCPFile, err)
	}
	return filepath.Join(h.StateRoot, rel, HostMCPFile), nil
}

// hostEnv is a bound Host session's env for its dir: the broker socket and the
// dir, the AWS block of a box with the dir's aws-config, and GitHub's helper,
// the SSH-to-HTTPS rewrites and the dir's gh config.
func hostEnv(bs config.Bindings, dir, realGH string) []string {
	env := []string{"AGENTBOX_BROKER=unix://" + dir + "/" + hostSockName, HostDirEnv + "=" + dir}
	env = append(env, awsEnv(bs, dir+"/aws-config")...)
	if bs.AWS != nil || len(bs.AWSConns) > 0 {
		env = append(env, "AWS_SHARED_CREDENTIALS_FILE="+dir+"/"+hostAWSCredsFile, tfPlanNoLock)
	}
	if bs.GitHub != nil {
		env = append(env, gitHubEnv(*bs.GitHub, "node "+shq(dir+"/agentbox-cred.js"), dir+"/"+hostGHDir, hostGitReset, hostGitRewrites)...)
		if realGH != "" {
			env = append(env, GHRealEnv+"="+realGH)
		}
	}
	return env
}

// HostStripEnv is every variable a bound Host session must not inherit before
// HostFor's env is added: StripEnv, StripGitHubEnv and the SSH rewrites' git
// config pair when there is a GitHub binding, and the names only Host sets.
func (h Hooks) HostStripEnv(bs config.Bindings) []string {
	s := StripEnv()
	if bs.GitHub != nil {
		s = append(s, StripGitHubEnv()...)
		// StripGitHubEnv covers 0..2; Host uses up to the push rewrite's index.
		for i := 3; i <= hostGitPushIndex; i++ {
			n := strconv.Itoa(i)
			s = append(s, "GIT_CONFIG_KEY_"+n, "GIT_CONFIG_VALUE_"+n)
		}
	}
	return append(s, HostDirEnv, GHRealEnv)
}

// ReleaseHost drops the reference HostFor took. remove deletes the session's
// dir once no reference is left (the session ended); without it the dir stays
// for the session to be found again (daemon shutdown).
func (h Hooks) ReleaseHost(sessionID string, remove bool) {
	if h.EP.Hosts == nil {
		return
	}
	if h.EP.Hosts.release(sessionID) || !remove {
		return
	}
	removeHostDir(h.StateRoot, HostDirName(sessionID))
}

// SweepHostDirs deletes every session dir under broker/h that belongs to no
// session in keep and to no session holding its socket: what a crash or an
// ended session left. Run at daemon start, after the live sessions are found
// again. Only names shaped like HostDirName are touched.
func (h Hooks) SweepHostDirs(keep []string) {
	root, err := os.OpenRoot(h.StateRoot)
	if err != nil {
		log.Printf("broker: sweeping host session dirs: %v", err)
		return
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(hostsRel))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("broker: sweeping host session dirs: %v", err)
		return
	}
	ents, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		log.Printf("broker: sweeping host session dirs: %v", err)
		return
	}
	live := map[string]bool{}
	if h.EP.Hosts != nil {
		live = h.EP.Hosts.held()
	}
	for _, id := range keep {
		live[HostDirName(id)] = true
	}
	for _, e := range ents {
		if name := e.Name(); hostDirNameRe.MatchString(name) && !live[name] {
			removeHostDirIn(root, name)
		}
	}
}

func removeHostDir(stateRoot, name string) {
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		log.Printf("broker: removing host session dir %s: %v", name, err)
		return
	}
	defer root.Close()
	removeHostDirIn(root, name)
}

// removeHostDirIn removes broker/h/<name> through root (RemoveAll never
// follows a symlink out of it).
func removeHostDirIn(root *os.Root, name string) {
	if err := root.RemoveAll(filepath.FromSlash(path.Join(hostsRel, name))); err != nil {
		log.Printf("broker: removing host session dir %s: %v", name, err)
	}
}

// writeAWSListingHosts rewrites aws.json in the binding dir when it exists and
// in each live Host dir of the same binding (names). A dir that is gone (its
// session ended meanwhile) is skipped.
func writeAWSListingHosts(stateRoot string, bs config.Bindings, base map[string]awsprobe.Result, names []string) error {
	if err := validateBindings(bs); err != nil {
		return err
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return fmt.Errorf("broker: opening state root: %w", err)
	}
	defer root.Close()
	rels := []string{filepath.Join("broker", "binding-"+bs.Hash())}
	for _, n := range names {
		rels = append(rels, filepath.Join(filepath.FromSlash(hostsRel), n))
	}
	var errs []error
	for _, rel := range rels {
		if _, err := root.Lstat(rel); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		errs = append(errs, writeAWSListingIn(root, rel, bs, base))
	}
	return errors.Join(errs...)
}
