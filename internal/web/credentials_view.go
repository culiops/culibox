package web

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/awsprobe"
	"github.com/culiops/culibox/internal/broker"
	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
	"github.com/culiops/culibox/internal/credref"
	"github.com/culiops/culibox/internal/secretchain"
	"github.com/culiops/culibox/internal/secretstore"
)

// Entry status on the wire. A reference is only ever looked at, never read: a
// store id by name, a file by credref.CheckFile, an env var by os.LookupEnv.
const (
	credOK           = "ok"
	credMissing      = "missing"
	credStoredLocked = "stored_locked"
	// credStoredUnavailable: the value is there but the Keychain did not give
	// the key (see credStoreBody.Unavailable for why and how to fix it).
	credStoredUnavailable = "stored_unavailable"
	credInvalid           = "invalid"
	credUnset             = "unset"
)

// credFileRefused is why a file reference is invalid: the file is there, but
// credref.ReadFile would refuse it.
const credFileRefused = "must be an absolute path to a regular file you own with mode 0600, not a symlink"

// credStoreReadError is shown instead of the text of a failed store listing: it
// can name a path.
const credStoreReadError = "could not read the store"

// credSnapshot is everything the read model needs from the manager, copied
// under its lock so the file and store lookups after it hold no lock.
type credSnapshot struct {
	// aws, apps and tokens are the rows to show: the merged entries (seeds first,
	// then web), then web entries that did not merge, flagged with their error.
	aws    []credAWSEntry
	apps   []credAppEntry
	tokens []credTokenEntry
	// refs maps each store id to the definitions that read it.
	refs    map[string][]string
	errs    []credfile.EntryError
	bad     bool
	secrets []string // secret names profiles list, sorted, de-duplicated
}

type credAWSEntry struct {
	conn   config.AWSConnection
	src    string
	err    string // non-empty: the entry did not merge
	usedBy []string
	base   awsprobe.Result
}

type credAppEntry struct {
	app    config.GitHubApp
	src    string
	err    string
	usedBy []string
}

type credTokenEntry struct {
	tok    config.StaticToken
	src    string
	err    string
	usedBy []string
}

// snapshot reads the manager's state once. The invalid list is derived from the
// file as it stands now, so it follows every save.
func (c *CredentialManager) snapshot() credSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _, errs := credfile.Merge(c.base, c.file)
	sn := credSnapshot{refs: c.storeRefsLocked(), errs: errs, bad: c.bad || c.readOnly != nil}
	errFor := func(kind, name string) string {
		for _, e := range errs {
			if e.Kind == kind && e.Name == name {
				return e.Err
			}
		}
		return "it is not valid"
	}
	used := func(kind, name string) []string {
		if c.pm == nil {
			return nil
		}
		return c.pm.RefsTo(kind, name)
	}
	// A web entry that did not merge and whose name nothing else holds still
	// needs a row, so it can be seen and deleted. One that shadows a seed or a
	// merged entry is only in the invalid list: a second row would carry the
	// same name.
	pending := func(kind, name string) bool { return c.src[kind+"/"+name] == "" }

	for _, a := range c.merged.AWSConnections {
		// An AssumeRole verdict never stands for an sso_direct connection: it has
		// no base role, and its read-only proof is the gate, run at every save,
		// spawn and mint.
		base := awsprobe.NotChecked()
		if !a.SSODirect() {
			base = awsprobe.Lookup(c.verdicts, verdictKey(a.SourceProfile, a.RoleARN))
		}
		sn.aws = append(sn.aws, credAWSEntry{conn: a, src: c.src[credfile.KindAWS+"/"+a.Name],
			usedBy: used(credfile.KindAWS, a.Name), base: base})
	}
	for _, a := range c.file.AWSConnections {
		if pending(credfile.KindAWS, a.Name) {
			sn.aws = append(sn.aws, credAWSEntry{conn: a, src: credfile.SourceWeb, err: errFor(credfile.KindAWS, a.Name),
				usedBy: used(credfile.KindAWS, a.Name), base: awsprobe.NotChecked()})
		}
	}
	for _, a := range c.merged.GitHubApps {
		sn.apps = append(sn.apps, credAppEntry{app: a, src: c.src[credfile.KindGitHubApp+"/"+a.Name], usedBy: used(credfile.KindGitHubApp, a.Name)})
	}
	for _, a := range c.file.GitHubApps {
		if pending(credfile.KindGitHubApp, a.Name) {
			sn.apps = append(sn.apps, credAppEntry{app: a, src: credfile.SourceWeb, err: errFor(credfile.KindGitHubApp, a.Name),
				usedBy: used(credfile.KindGitHubApp, a.Name)})
		}
	}
	for _, t := range c.merged.StaticTokens {
		sn.tokens = append(sn.tokens, credTokenEntry{tok: t, src: c.src[credfile.KindToken+"/"+t.Name], usedBy: used(credfile.KindToken, t.Name)})
	}
	for _, t := range c.file.StaticTokens {
		if pending(credfile.KindToken, t.Name) {
			sn.tokens = append(sn.tokens, credTokenEntry{tok: t, src: credfile.SourceWeb, err: errFor(credfile.KindToken, t.Name),
				usedBy: used(credfile.KindToken, t.Name)})
		}
	}
	if c.pm != nil {
		for _, p := range c.pm.All() {
			sn.secrets = append(sn.secrets, p.Secrets...)
		}
		slices.Sort(sn.secrets)
		sn.secrets = slices.Compact(sn.secrets)
	}
	return sn
}

// Wire shapes. Every list is non-nil so it marshals as [] and never null.

type credStoreBody struct {
	Initialized    bool       `json:"initialized"`
	Locked         bool       `json:"locked"`
	ExpiresAt      *time.Time `json:"expires_at"`
	Platform       string     `json:"platform"`
	PassphraseMode bool       `json:"passphrase_mode"`
	// Unavailable is set (omitted otherwise) when the macOS Keychain did not
	// give the store key; Locked is then false.
	Unavailable *storeUnavailableBody `json:"unavailable,omitempty"`
}

type credElevateBody struct {
	RoleARN    string `json:"role_arn"`
	MaxMinutes int    `json:"max_minutes"`
	AllowIAM   bool   `json:"allow_iam"`
	// BoxBucketPrefix has no form field; the Edit form sends it back as read.
	BoxBucketPrefix string `json:"box_bucket_prefix,omitempty"`
	// PassRolePrefix (#302) is edited in the form.
	PassRolePrefix string `json:"pass_role_prefix,omitempty"`
}

type credAWSBody struct {
	Name          string           `json:"name"`
	Label         string           `json:"label"`
	Description   string           `json:"description"`
	AccountID     string           `json:"account_id"`
	Region        string           `json:"region"`
	Prod          bool             `json:"prod"`
	Source        string           `json:"source"`
	SourceProfile string           `json:"source_profile"`
	RoleARN       string           `json:"role_arn"`
	Mode          string           `json:"mode,omitempty"` // "" = assume_role
	Elevate       *credElevateBody `json:"elevate"`
	// EgressAllow and S3Read are what the Edit form must send back unchanged:
	// a save replaces the whole connection, so a form that did not know them
	// would silently drop them.
	EgressAllow []string    `json:"egress_allow,omitempty"`
	S3Read      []string    `json:"s3_read,omitempty"`
	Status      string      `json:"status"`
	Error       string      `json:"error,omitempty"`
	UsedBy      []string    `json:"used_by"`
	Base        verdictBody `json:"base"`
}

type credAppBody struct {
	Name       string   `json:"name"`
	ClientID   string   `json:"client_id"`
	KeyRefKind string   `json:"key_ref_kind"`
	Source     string   `json:"source"`
	Status     string   `json:"status"`
	Error      string   `json:"error,omitempty"`
	UsedBy     []string `json:"used_by"`
}

type credTokenBody struct {
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Template    string   `json:"template"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Prod        bool     `json:"prod"`
	Env         []string `json:"env"`
	Hosts       []string `json:"hosts"`
	ValueKind   string   `json:"value_kind"`
	Source      string   `json:"source"`
	Status      string   `json:"status"`
	Error       string   `json:"error,omitempty"`
	UsedBy      []string `json:"used_by"`
	// Entry is the saved definition of a web-created token, for the Edit form to
	// send back with only its label, description or prod flag changed (the
	// server refuses any other change to a template token). It holds references
	// and non-secret settings only, like the rest of this body. A config.json
	// seed has none: only the file edits it.
	Entry *config.StaticToken `json:"entry,omitempty"`
}

type credValueBody struct {
	ID           string     `json:"id"`
	Updated      *time.Time `json:"updated"`
	ReferencedBy []string   `json:"referenced_by"`
}

type credLegacyBody struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type credInvalidBody struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

type credViewBody struct {
	Store         credStoreBody     `json:"store"`
	AWS           []credAWSBody     `json:"aws"`
	GitHubApps    []credAppBody     `json:"github_apps"`
	Tokens        []credTokenBody   `json:"tokens"`
	Values        []credValueBody   `json:"values"`
	LegacySecrets []credLegacyBody  `json:"legacy_secrets"`
	FileBad       bool              `json:"file_bad"`
	Invalid       []credInvalidBody `json:"invalid"`
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// accountOf is the 12-digit account id inside a role ARN, or "".
func accountOf(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) >= 6 && parts[0] == "arn" && credAccountRe.MatchString(parts[4]) {
		return parts[4]
	}
	return ""
}

// connAccount is the account a connection's row shows: the role ARN's for
// assume_role; for sso_direct (no role ARN) the source profile's
// sso_account_id read from the host's AWS files, "" when unreadable.
func connAccount(c config.AWSConnection) string {
	if !c.SSODirect() {
		return accountOf(c.RoleARN)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	id, err := broker.SSOAccountOf(ctx, c.SourceProfile)
	if err != nil {
		return ""
	}
	return id
}

// hostLabel is how an egress rule reads on the card: host, host:port for a
// non-default port, or the CIDR.
func hostLabel(h config.TokenEgress) string {
	if h.CIDR != "" {
		return h.CIDR
	}
	if p := h.EffectivePort(); p != 443 {
		return h.Host + ":" + strconv.Itoa(p)
	}
	return h.Host
}

// storeIDs is what the store holds right now, by id. listErr is set when it
// could not be listed for a reason other than "never created".
type storeIDs struct {
	ids     map[string]secretstore.Entry
	listErr bool
	locked  bool
	// unavailable: the Keychain did not give the store key.
	unavailable bool
}

func (s storeIDs) has(id string) bool { _, ok := s.ids[id]; return ok }

// refStatus says whether a reference points at something, without reading it:
// a store id is looked up by name, a file goes through credref.CheckFile (never opened), an env
// var is looked up (its value is dropped unread).
func (s storeIDs) refStatus(r config.KeyRef) (status, errText string) {
	switch r.Type {
	case "store":
		switch {
		case s.listErr:
			return credMissing, credStoreReadError
		case !s.has(r.ID):
			return credMissing, ""
		case s.unavailable:
			return credStoredUnavailable, ""
		case s.locked:
			return credStoredLocked, ""
		}
		return credOK, ""
	case "file":
		// The gate ReadFile runs before it opens the file, and nothing more: the
		// file is never opened, so "ok" means a spawn would get past the checks.
		switch err := credref.CheckFile(r.Path); {
		case err == nil:
			return credOK, ""
		case errors.Is(err, fs.ErrNotExist):
			return credMissing, ""
		}
		// The refusal's own text names the path: show a fixed sentence instead.
		return credInvalid, credFileRefused
	case "env":
		// The value is dropped unread; an empty variable is unset, as when resolved.
		if v, set := os.LookupEnv(r.Var); set && v != "" {
			return credOK, ""
		}
		return credUnset, ""
	}
	return credInvalid, "no value reference"
}

// credView builds the answer from a snapshot and the store.
func (s *Server) credView() credViewBody {
	sn := s.Creds.snapshot()
	body := credViewBody{
		AWS: []credAWSBody{}, GitHubApps: []credAppBody{}, Tokens: []credTokenBody{},
		Values: []credValueBody{}, LegacySecrets: []credLegacyBody{}, Invalid: []credInvalidBody{},
		FileBad: sn.bad,
	}
	var locked, unavailable bool
	if s.Store != nil {
		st := s.Store.Status()
		locked, unavailable = st.Locked, st.Unavailable != nil
		body.Store = credStoreBody{Initialized: st.Initialized, Locked: st.Locked, Platform: st.Platform, PassphraseMode: st.PassphraseMode,
			Unavailable: unavailableBody(st.Unavailable)}
		if !st.ExpiresAt.IsZero() {
			t := st.ExpiresAt
			body.Store.ExpiresAt = &t
		}
	}

	ids := storeIDs{ids: map[string]secretstore.Entry{}, locked: locked, unavailable: unavailable}
	if ents, err := s.Creds.vals.List(); err == nil {
		for _, e := range ents {
			ids.ids[e.ID] = e
		}
	} else if !errors.Is(err, secretstore.ErrNotInitialized) {
		ids.listErr = true
	}

	for _, e := range sn.aws {
		b := credAWSBody{Name: e.conn.Name, Label: e.conn.Label, Description: e.conn.Description, AccountID: connAccount(e.conn),
			Region: e.conn.Region, Prod: e.conn.Prod, Source: e.src, SourceProfile: e.conn.SourceProfile, RoleARN: e.conn.RoleARN, Mode: e.conn.Mode,
			EgressAllow: slices.Clone(e.conn.EgressAllow), S3Read: slices.Clone(e.conn.S3Read),
			Status: credOK, Error: e.err, UsedBy: orEmpty(e.usedBy), Base: verdictOf(e.base)}
		if e.err != "" {
			b.Status = credInvalid
		}
		if el := e.conn.Elevate; el != nil {
			b.Elevate = &credElevateBody{RoleARN: el.RoleARN, MaxMinutes: el.Max(), AllowIAM: el.AllowIAM, BoxBucketPrefix: el.BoxBucketPrefix, PassRolePrefix: el.PassRolePrefix}
		}
		body.AWS = append(body.AWS, b)
	}
	for _, e := range sn.apps {
		b := credAppBody{Name: e.app.Name, ClientID: e.app.ClientID, KeyRefKind: e.app.PrivateKey.Type, Source: e.src, UsedBy: orEmpty(e.usedBy)}
		b.Status, b.Error = ids.refStatus(e.app.PrivateKey)
		if e.err != "" {
			b.Status, b.Error = credInvalid, e.err
		}
		body.GitHubApps = append(body.GitHubApps, b)
	}
	for _, e := range sn.tokens {
		t := e.tok
		b := credTokenBody{Name: t.Name, Provider: t.Provider, Template: t.Template, Label: t.Label, Description: t.Description,
			Prod: t.Prod, Env: orEmpty(slices.Clone(t.Env)), Hosts: []string{}, ValueKind: t.Value.Type, Source: e.src, UsedBy: orEmpty(e.usedBy)}
		for _, h := range t.Egress {
			b.Hosts = append(b.Hosts, hostLabel(h))
		}
		b.Status, b.Error = ids.refStatus(t.Value)
		if e.err != "" {
			b.Status, b.Error = credInvalid, e.err
		}
		if e.src == credfile.SourceWeb {
			c := t.Clone()
			b.Entry = &c
		}
		body.Tokens = append(body.Tokens, b)
	}

	for _, id := range slices.Sorted(maps.Keys(ids.ids)) {
		v := credValueBody{ID: id, ReferencedBy: orEmpty(sn.refs[id])}
		if u := ids.ids[id].Updated; !u.IsZero() {
			v.Updated = &u
		}
		body.Values = append(body.Values, v)
	}

	for _, name := range sn.secrets {
		st := credMissing
		if s.SecretCheck != nil {
			switch p, err := s.SecretCheck(name); {
			case err != nil:
				st = credInvalid // the error text can name a path: not shown
			case p == secretchain.Present:
				st = credOK
			case p == secretchain.StoredLocked:
				st = credStoredLocked
			case p == secretchain.StoredUnavailable:
				st = credStoredUnavailable
			}
		}
		body.LegacySecrets = append(body.LegacySecrets, credLegacyBody{Name: name, Status: st})
	}

	for _, e := range sn.errs {
		body.Invalid = append(body.Invalid, credInvalidBody{Kind: e.Kind, Name: e.Name, Error: e.Err})
	}
	return body
}

// handleCredentials is GET /api/credentials: the screen's read model. Cookie
// only, like every credentials route. It carries references and states, never a
// value, a file's contents or an environment variable's value.
func (s *Server) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if s.Creds == nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !credGate(w, r, false) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.credView())
}
