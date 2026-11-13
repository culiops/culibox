// Package credfile holds the credential entries defined from the web screen:
// <dataDir>/credentials.json, the same pattern as profiles.json. config.json
// entries stay read-only seeds; Merge appends the web entries after them so
// every consumer (daemon, doctor, store CLI) works on one config.Config.
//
// The file never holds a secret value, only references to where one lives
// (config.KeyRef), exactly like the entries in config.json.
package credfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/culiops/culibox/internal/config"
)

// File is the on-disk shape of credentials.json.
type File struct {
	AWSConnections []config.AWSConnection `json:"aws_connections,omitempty"`
	GitHubApps     []config.GitHubApp     `json:"github_apps,omitempty"`
	StaticTokens   []config.StaticToken   `json:"static_tokens,omitempty"`

	// Undecodable are entries Load could not decode (e.g. an unknown field).
	// Merge reports them. They are not written back, so a file that has any
	// cannot be saved over (CanSave): a save would drop them from disk.
	Undecodable []EntryError `json:"-"`
	// NotUnderstood is one line per top-level key this build does not know, per
	// known kind that is not a list, or for the whole file when it cannot be
	// read at all. A file with any of these is never saved over (CanSave).
	NotUnderstood []string `json:"-"`
}

// ErrNotUnderstood marks a credentials.json with content this build cannot
// read. Saving would drop that content, so nothing saves over it.
var ErrNotUnderstood = errors.New("credentials.json has content this CuliBox cannot read")

// CanSave is nil when everything in the file was understood, so a save loses
// nothing. Otherwise it wraps ErrNotUnderstood and names the first reason.
func (f File) CanSave() error {
	switch {
	case len(f.NotUnderstood) > 0:
		return fmt.Errorf("%w: %s", ErrNotUnderstood, f.NotUnderstood[0])
	case len(f.Undecodable) > 0:
		e := f.Undecodable[0]
		return fmt.Errorf("%w: %s %s: %s", ErrNotUnderstood, e.Kind, e.Name, e.Err)
	}
	return nil
}

// Entry kinds, as used in Sources keys and EntryError.Kind.
const (
	KindAWS       = "aws"
	KindGitHubApp = "github_app"
	KindToken     = "token"
)

// Where an entry came from.
const (
	SourceConfig = "config"
	SourceWeb    = "web"
)

// Sources maps "<kind>/<name>" to SourceConfig or SourceWeb.
type Sources map[string]string

// EntryError is a web entry Merge skipped, and why. Err never carries a value.
type EntryError struct{ Kind, Name, Err string }

// Path is where the file lives.
func Path(dataDir string) string { return filepath.Join(dataDir, "credentials.json") }

// decodeStrict is json decoding that refuses unknown fields and trailing data.
func decodeStrict(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// decodeEntries decodes each entry strictly. An unknown field is a typo (or a
// newer version's field) in a security-relevant definition (egress, env), and
// silently ignoring it would widen or narrow what the entry does, so that entry
// is reported and left out; the others are kept.
func decodeEntries[T any](kind string, raws []json.RawMessage, bad *[]EntryError) []T {
	var out []T
	for i, r := range raws {
		var e T
		err := decodeStrict(r, &e)
		if err == nil {
			out = append(out, e)
			continue
		}
		var n struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(r, &n) // best effort: only to label the error
		if n.Name == "" {
			n.Name = fmt.Sprintf("#%d", i)
		}
		*bad = append(*bad, EntryError{kind, n.Name, err.Error()})
	}
	return out
}

// knownKinds are the top-level keys this build reads, in the order their
// problems are reported.
var knownKinds = []string{"aws_connections", "github_apps", "static_tokens"}

// parse is file-level strict (not JSON, not an object, trailing data: the whole
// file is bad) and entry-level tolerant (see decodeEntries). A top-level key it
// does not know is recorded, not fatal: what it does understand still loads. A
// known key that is not a list loads nothing for that kind and makes the file
// bad, with the other kinds still loaded. err is set only when bad is.
func parse(b []byte) (f File, bad bool, err error) {
	var top map[string]json.RawMessage
	if err := decodeStrict(b, &top); err != nil {
		return File{NotUnderstood: []string{err.Error()}}, true, err
	}
	lists := map[string][]json.RawMessage{}
	var wrong []string
	for _, k := range knownKinds {
		raw, ok := top[k]
		if !ok {
			continue
		}
		var l []json.RawMessage
		if err := json.Unmarshal(raw, &l); err != nil {
			wrong = append(wrong, k)
			f.NotUnderstood = append(f.NotUnderstood, fmt.Sprintf("%q is not a list of entries", k))
			continue
		}
		lists[k] = l
	}
	var unknown []string
	for k := range top {
		if !slices.Contains(knownKinds, k) {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		f.NotUnderstood = append(f.NotUnderstood, fmt.Sprintf("unknown top-level key %q", k))
	}
	f.AWSConnections = decodeEntries[config.AWSConnection](KindAWS, lists["aws_connections"], &f.Undecodable)
	f.GitHubApps = decodeEntries[config.GitHubApp](KindGitHubApp, lists["github_apps"], &f.Undecodable)
	f.StaticTokens = decodeEntries[config.StaticToken](KindToken, lists["static_tokens"], &f.Undecodable)
	if len(wrong) > 0 {
		return f, true, fmt.Errorf("%s is not a list", strings.Join(wrong, ", "))
	}
	return f, false, nil
}

// Load reads the file. Missing is an empty File. A file that cannot be read or
// is not a JSON object gives a File whose NotUnderstood holds the reason,
// bad=true and an error saying why; a known kind of the wrong type is bad too,
// but the other kinds still load. The daemon still starts either way, and Save
// refuses to write over such a file.
func Load(dataDir string) (f File, bad bool, err error) {
	b, err := os.ReadFile(Path(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return File{}, false, nil
	}
	if err != nil {
		return File{NotUnderstood: []string{err.Error()}}, true, fmt.Errorf("credentials file: %w", err)
	}
	f, bad, err = parse(b)
	if err != nil {
		return f, true, fmt.Errorf("credentials file %s is not valid: %w", Path(dataDir), err)
	}
	return f, false, nil
}

// Save writes the file atomically: tmp 0600, fsync, rename, directory fsync
// (the same steps as store.flushLocked). A current file with content this build
// cannot read is never written over (ErrNotUnderstood): it may be a newer
// CuliBox's data, or something a human wants to repair.
func Save(dataDir string, f File) error {
	path := Path(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if cur, _, _ := Load(dataDir); cur.CanSave() != nil {
		return cur.CanSave()
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	w, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := w.Chmod(0o600); err != nil { // a leftover tmp keeps its old mode otherwise
		w.Close()
		return err
	}
	if _, err := w.Write(b); err != nil {
		w.Close()
		return err
	}
	if err := w.Sync(); err != nil { // durable before the rename
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil { // atomic replace
		return err
	}
	if d, err := os.Open(dataDir); err == nil { // make the rename durable
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ValidateMerged runs the list-level checks Config.Validate runs on the
// credential lists, on a complete config. The web manager calls it before
// every write.
func ValidateMerged(cfg config.Config) error {
	if err := config.ValidateGitHubApps(cfg.GitHubApps); err != nil {
		return err
	}
	if err := config.ValidateStaticTokens(cfg.StaticTokens); err != nil {
		return err
	}
	if err := config.ValidateAWSConnections(cfg.AWSConnections); err != nil {
		return err
	}
	return config.CheckPasswordEnvClash(cfg.PasswordEnv, cfg.StaticTokens)
}

// candidate is one web entry waiting to be merged.
type candidate[T any] struct {
	entry T
	name  string
	err   error
}

// mergeKind appends the web entries to seeds, one at a time, keeping each only
// if the whole list still passes check. Passes repeat while any entry gets in,
// because an entry may depend on one later in the file (a token's mcp.extra).
// What cannot get in is reported with its last error.
func mergeKind[T any](kind string, seeds, web []T, nameOf func(T) string, check func([]T) error,
	src Sources, errs *[]EntryError) []T {
	merged := slices.Clone(seeds)
	seedName := map[string]bool{}
	for _, s := range seeds {
		seedName[nameOf(s)] = true
	}
	var pending []candidate[T]
	for _, e := range web {
		n := nameOf(e)
		if seedName[n] {
			*errs = append(*errs, EntryError{kind, n, fmt.Sprintf("name %q is already defined in config.json; a web entry cannot shadow it", n)})
			continue
		}
		pending = append(pending, candidate[T]{entry: e, name: n})
	}
	for progress := true; progress && len(pending) > 0; {
		progress = false
		var next []candidate[T]
		for _, c := range pending {
			trial := append(slices.Clone(merged), c.entry)
			if c.err = check(trial); c.err != nil {
				next = append(next, c)
				continue
			}
			merged = trial
			src[kind+"/"+c.name] = SourceWeb
			progress = true
		}
		pending = next
	}
	for _, c := range pending {
		*errs = append(*errs, EntryError{kind, c.name, c.err.Error()})
	}
	return merged
}

// Merge appends the web entries to cfg's seeds. cfg is not modified. A web
// entry is skipped, with an EntryError, when it fails its own validation,
// reuses a seed's name, or makes the merged list fail the list checks: the
// daemon must start with a bad file so the screen that fixes it stays reachable.
func Merge(cfg config.Config, f File) (merged config.Config, src Sources, errs []EntryError) {
	merged = cfg
	src = Sources{}
	errs = slices.Clone(f.Undecodable)
	for _, c := range cfg.AWSConnections {
		src[KindAWS+"/"+c.Name] = SourceConfig
	}
	for _, a := range cfg.GitHubApps {
		src[KindGitHubApp+"/"+a.Name] = SourceConfig
	}
	for _, t := range cfg.StaticTokens {
		src[KindToken+"/"+t.Name] = SourceConfig
	}
	merged.AWSConnections = mergeKind(KindAWS, cfg.AWSConnections, f.AWSConnections,
		func(c config.AWSConnection) string { return c.Name },
		config.ValidateAWSConnections, src, &errs)
	merged.GitHubApps = mergeKind(KindGitHubApp, cfg.GitHubApps, f.GitHubApps,
		func(a config.GitHubApp) string { return a.Name },
		config.ValidateGitHubApps, src, &errs)
	merged.StaticTokens = mergeKind(KindToken, cfg.StaticTokens, f.StaticTokens,
		func(t config.StaticToken) string { return t.Name },
		func(l []config.StaticToken) error {
			if err := config.ValidateStaticTokens(l); err != nil {
				return err
			}
			return config.CheckPasswordEnvClash(cfg.PasswordEnv, l)
		}, src, &errs)
	return merged, src, errs
}
