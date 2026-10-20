package web

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/culiops/agent-box/internal/account"
	"github.com/culiops/agent-box/internal/awsprobe"
	"github.com/culiops/agent-box/internal/broker"
	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credfile"
	"github.com/culiops/agent-box/internal/secretstore"
	"github.com/culiops/agent-box/internal/storerefs"
)

// CredValues is the encrypted value store as the credentials screen uses it.
// *secretstore.Store satisfies it; Set and Remove work while it is locked.
type CredValues interface {
	Set(id string, v []byte, actor string) error
	Remove(id, actor string) error
	List() ([]secretstore.Entry, error)
}

// CredRecorder appends one credential-definition audit row (*broker.Audit).
// An error means the row was not written, and the change it describes must
// not happen.
type CredRecorder interface {
	RecordCred(broker.CredEvent) error
}

// BaseRoleChecker measures whether an AWS base role can write (the broker's
// ProbeRole behind a timeout). nil = no broker is running.
type BaseRoleChecker interface {
	Check(ctx context.Context, t broker.ProbeTarget) awsprobe.Result
}

// Errors of the CredentialManager. None of them ever carries a secret value.
var (
	ErrCredExists         = errors.New("already defined")
	ErrCredSeed           = errors.New("defined in config.json")
	ErrCredNotFound       = errors.New("not defined")
	ErrNoBroker           = errors.New("AWS connections need the boxed runtime")
	ErrEnvRefOnWeb        = errors.New(`an "env" reference cannot be set from the web (a new variable only reaches the daemon after a restart); use the store or a file`)
	ErrPrivateEgressOnWeb = errors.New("private egress hosts (private/cidr) can only be defined in config.json")
	// ErrMCPOnWeb: web tokens carry no mcp server, because the config bundle
	// that runs MCP commands is built at startup only (see credLive).
	ErrMCPOnWeb = errors.New("an mcp server can only be defined in config.json")
)

// ErrCredPersist marks a failure to record the audit row or write the file: its
// text can name a path, so a route shows a fixed sentence and logs the rest.
var ErrCredPersist = errors.New("could not record or save the change")

// ErrCheckLimited refuses an AWS save whose server-side base-role check would
// exceed the daemon-wide budget for such checks. Wait is until the window ends.
type ErrCheckLimited struct{ Wait time.Duration }

func (e ErrCheckLimited) Error() string { return "too many base-role checks: try again shortly" }

// saveCheckMax is how many base-role checks the create/replace path may run
// per minute, daemon-wide (the routes that check on request have their own,
// smaller budget).
const saveCheckMax = 60

// ErrCredInUse refuses a delete (or a value overwrite) while other definitions
// still use the entry or value. By names them, sorted.
type ErrCredInUse struct{ By []string }

func (e ErrCredInUse) Error() string { return "in use by " + strings.Join(e.By, ", ") }

// ErrNeedsAck refuses an AWS save whose base role can write, until the user
// acknowledges it (D4). Result is the verdict to show.
type ErrNeedsAck struct{ Result awsprobe.Result }

func (e ErrNeedsAck) Error() string {
	return "the base role can write to this AWS account; acknowledge to save"
}

// tokenTemplateRe bounds StaticToken.Template on a web entry: empty, or a
// short catalog-id-shaped string.
var tokenTemplateRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// kindLabel names a credfile kind in messages.
var kindLabel = map[string]string{
	credfile.KindAWS:       "AWS connection",
	credfile.KindGitHubApp: "GitHub App",
	credfile.KindToken:     "token",
}

// CredentialManager owns the web-defined credential entries
// (<dataDir>/credentials.json): it creates, replaces and deletes them live.
// One mutex serialises every method. A write builds the candidate file, merges
// it over the config.json seeds and validates the result, records the audit
// row (no row, no change), saves the file, then pushes the merged config
// through apply, all inside that one critical section, so two saves can never
// reach the holders out of order.
//
// Lock order: CredentialManager.mu, then ProfileManager.mu (RefsTo, and the
// setters apply calls). The profile manager never calls back in here.
type CredentialManager struct {
	mu       sync.Mutex
	base     config.Config // config.json only: the seeds
	file     credfile.File
	merged   config.Config
	src      credfile.Sources
	dataDir  string
	pm       *ProfileManager
	vals     CredValues
	rec      CredRecorder
	chk      BaseRoleChecker
	apply    func(config.Config)
	accounts func() []account.Account
	// verdicts is the last base-role check per (source_profile, role_arn). In
	// memory only: after a restart every connection is "not checked".
	verdicts map[string]awsprobe.Result
	// checkLimit charges the base-role checks a save runs itself.
	checkLimit callLimiter
}

// NewCredentialManager wires the manager. base must be the config as read from
// config.json, NOT already merged with the credentials file (Merge would then
// see every web entry as a seed). web is what credfile.Load returned. apply is
// credLive.Apply; the constructor does not call it (boot already applied the
// merged config). accounts lists the agent accounts for the store-reference
// scan (AccountsOf); nil = none.
func NewCredentialManager(base config.Config, web credfile.File, dataDir string, pm *ProfileManager, vals CredValues,
	rec CredRecorder, chk BaseRoleChecker, apply func(config.Config), accounts func() []account.Account) *CredentialManager {
	c := &CredentialManager{base: base, file: web, dataDir: dataDir, pm: pm, vals: vals, rec: rec, chk: chk,
		apply: apply, accounts: accounts, verdicts: map[string]awsprobe.Result{},
		checkLimit: callLimiter{max: saveCheckMax, window: time.Minute}}
	c.merged, c.src, _ = credfile.Merge(base, web)
	return c
}

// AccountsOf lists the non-default agent accounts of s, for NewCredentialManager.
func AccountsOf(s *account.Store) func() []account.Account {
	return func() []account.Account {
		if s == nil {
			return nil
		}
		var out []account.Account
		for _, r := range s.List() {
			if !r.IsDefault {
				out = append(out, r.Account)
			}
		}
		return out
	}
}

// Merged is the effective config (seeds plus web entries) and where each entry
// came from. Callers must not modify it.
func (c *CredentialManager) Merged() (config.Config, credfile.Sources) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.merged, maps.Clone(c.src)
}

func verdictKey(sourceProfile, roleARN string) string { return sourceProfile + "\x00" + roleARN }

// maxVerdicts bounds the verdict map; past it an arbitrary old verdict is
// dropped (it then reads "not checked", which is honest).
const maxVerdicts = 512

// setVerdictLocked keeps r for (sourceProfile, roleARN). Caller holds c.mu.
func (c *CredentialManager) setVerdictLocked(sourceProfile, roleARN string, r awsprobe.Result) {
	k := verdictKey(sourceProfile, roleARN)
	if _, ok := c.verdicts[k]; !ok && len(c.verdicts) >= maxVerdicts {
		for old := range c.verdicts {
			delete(c.verdicts, old)
			break
		}
	}
	c.verdicts[k] = r.Normalize()
}

// Verdict is the last base-role check of (sourceProfile, roleARN), or
// NotChecked.
func (c *CredentialManager) Verdict(sourceProfile, roleARN string) awsprobe.Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	return awsprobe.Lookup(c.verdicts, verdictKey(sourceProfile, roleARN))
}

// RecordVerdict keeps the result of a base-role check made elsewhere (the
// check route), so the entries list shows it.
func (c *CredentialManager) RecordVerdict(sourceProfile, roleARN string, r awsprobe.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setVerdictLocked(sourceProfile, roleARN, r)
}

// isSeed reports whether config.json defines kind/name.
func (c *CredentialManager) isSeed(kind, name string) bool {
	switch kind {
	case credfile.KindAWS:
		return slices.ContainsFunc(c.base.AWSConnections, func(x config.AWSConnection) bool { return x.Name == name })
	case credfile.KindGitHubApp:
		return slices.ContainsFunc(c.base.GitHubApps, func(x config.GitHubApp) bool { return x.Name == name })
	case credfile.KindToken:
		return slices.ContainsFunc(c.base.StaticTokens, func(x config.StaticToken) bool { return x.Name == name })
	}
	return false
}

// fileIndex is the position of kind/name in the credentials file, or -1. An
// entry that does not currently merge (invalid) still occupies its name.
func (c *CredentialManager) fileIndex(kind, name string) int {
	switch kind {
	case credfile.KindAWS:
		return slices.IndexFunc(c.file.AWSConnections, func(x config.AWSConnection) bool { return x.Name == name })
	case credfile.KindGitHubApp:
		return slices.IndexFunc(c.file.GitHubApps, func(x config.GitHubApp) bool { return x.Name == name })
	case credfile.KindToken:
		return slices.IndexFunc(c.file.StaticTokens, func(x config.StaticToken) bool { return x.Name == name })
	}
	return -1
}

// presence is the create/replace/seed rule shared by every Put.
func (c *CredentialManager) presence(kind, name string, create bool) error {
	if c.isSeed(kind, name) {
		return fmt.Errorf("%s %q is %w", kindLabel[kind], name, ErrCredSeed)
	}
	exists := c.fileIndex(kind, name) >= 0
	if create && exists {
		return fmt.Errorf("%s %q is %w", kindLabel[kind], name, ErrCredExists)
	}
	if !create && !exists {
		return fmt.Errorf("%s %q is %w", kindLabel[kind], name, ErrCredNotFound)
	}
	return nil
}

// cloneFile is a copy of the file whose lists can be changed without touching
// c.file. Undecodable is dropped: the next Save drops it from disk.
func (c *CredentialManager) cloneFile() credfile.File {
	return credfile.File{
		AWSConnections: slices.Clone(c.file.AWSConnections),
		GitHubApps:     slices.Clone(c.file.GitHubApps),
		StaticTokens:   slices.Clone(c.file.StaticTokens),
	}
}

// checkLocked merges cand over the seeds and validates it. Every key in want
// ("<kind>/<name>") must be merged as a web entry; no web entry merged today
// may drop out, except the keys in gone (a delete). Caller holds c.mu.
func (c *CredentialManager) checkLocked(cand credfile.File, want, gone []string) (config.Config, credfile.Sources, error) {
	merged, src, errs := credfile.Merge(c.base, cand)
	errFor := func(key string) string {
		for _, e := range errs {
			if e.Kind+"/"+e.Name == key {
				return e.Err
			}
		}
		return "it is not valid"
	}
	for _, k := range want {
		if src[k] != credfile.SourceWeb {
			return config.Config{}, nil, errors.New(errFor(k))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(c.src)) {
		if c.src[k] == credfile.SourceWeb && src[k] != credfile.SourceWeb && !slices.Contains(gone, k) {
			kind, name, _ := strings.Cut(k, "/")
			return config.Config{}, nil, fmt.Errorf("this change would make %s %q invalid: %s", kindLabel[kind], name, errFor(k))
		}
	}
	if err := credfile.ValidateMerged(merged); err != nil {
		return config.Config{}, nil, err
	}
	if err := c.checkProfilesLocked(merged); err != nil {
		return config.Config{}, nil, err
	}
	return merged, src, nil
}

// checkProfilesLocked runs, for every profile (seed and runtime, and each
// config.json seed even where a runtime profile overrides it), the same
// per-profile credential check Config.Validate runs at boot
// (config.CheckCredentialRefs) against the candidate merged config. A save may
// not break a profile: a seed it broke would stop the next boot, a runtime one
// would stop spawning. A profile that already fails today (a save that raced a
// delete) does not block unrelated saves; only a newly broken one refuses.
// Caller holds c.mu.
func (c *CredentialManager) checkProfilesLocked(merged config.Config) error {
	optDefs := map[string]config.OptionDef{}
	for _, o := range c.base.Options {
		optDefs[o.Key] = o
	}
	// The effective profiles, then the config.json seeds: boot validates a
	// seed even when a runtime profile of the same name overrides it. A name
	// is reported once.
	var broken []string
	seen := map[string]bool{}
	var profiles []config.AgentConfig
	if c.pm != nil {
		profiles = c.pm.All()
	}
	for _, p := range append(profiles, c.base.Agents...) {
		if seen[p.Name] {
			continue
		}
		if err := config.CheckCredentialRefs(p, merged, optDefs); err != nil &&
			config.CheckCredentialRefs(p, c.merged, optDefs) == nil {
			seen[p.Name] = true
			broken = append(broken, fmt.Sprintf("profile %q: %v", p.Name, err))
		}
	}
	if len(broken) > 0 {
		return fmt.Errorf("this change would break %s", strings.Join(broken, "; "))
	}
	return nil
}

// persistLocked records the audit rows, saves the file and applies the merged
// config. A failed row or save changes nothing. Caller holds c.mu.
func (c *CredentialManager) persistLocked(cand credfile.File, merged config.Config, src credfile.Sources, events []broker.CredEvent) error {
	for _, e := range events {
		e.Actor = storeActor
		if err := c.rec.RecordCred(e); err != nil {
			return fmt.Errorf("%w: audit: %w", ErrCredPersist, err)
		}
	}
	if err := credfile.Save(c.dataDir, cand); err != nil {
		return fmt.Errorf("%w: save credentials: %w", ErrCredPersist, err)
	}
	c.file, c.merged, c.src = cand, merged, src
	c.apply(merged)
	return nil
}

// storeRefsLocked maps each store id to the definitions that read it: the
// merged entries (plus web entries that do not merge today: they still mean
// to use their value), every profile's secrets, and the accounts.
func (c *CredentialManager) storeRefsLocked() map[string][]string {
	cfg := c.merged
	cfg.GitHubApps = slices.Clone(cfg.GitHubApps)
	cfg.StaticTokens = slices.Clone(cfg.StaticTokens)
	for _, a := range c.file.GitHubApps {
		if c.src[credfile.KindGitHubApp+"/"+a.Name] != credfile.SourceWeb {
			cfg.GitHubApps = append(cfg.GitHubApps, a)
		}
	}
	for _, t := range c.file.StaticTokens {
		if c.src[credfile.KindToken+"/"+t.Name] != credfile.SourceWeb {
			cfg.StaticTokens = append(cfg.StaticTokens, t)
		}
	}
	if c.pm != nil {
		cfg.Agents = c.pm.All()
	}
	var accts []account.Account
	if c.accounts != nil {
		accts = c.accounts()
	}
	return storerefs.Refs(cfg, accts)
}

// setAndPersistLocked stores the values first, then persists; if anything
// fails after a Set, it removes the ids this call created (never one that was
// already in the store). owner maps each id to the ref ("static_tokens/<name>")
// allowed to share it: a new value may not overwrite a value that any other
// definition reads.
func (c *CredentialManager) setAndPersistLocked(values map[string][]byte, owner map[string]string,
	cand credfile.File, merged config.Config, src credfile.Sources, events []broker.CredEvent) error {
	if len(values) > 0 {
		refs := c.storeRefsLocked()
		for _, id := range slices.Sorted(maps.Keys(values)) {
			others := slices.DeleteFunc(slices.Clone(refs[id]), func(r string) bool { return r == owner[id] })
			if len(others) > 0 {
				return fmt.Errorf("store value %q: %w", id, ErrCredInUse{By: others})
			}
		}
	}
	pre := map[string]bool{}
	if len(values) > 0 {
		ents, err := c.vals.List()
		if err != nil {
			return fmt.Errorf("store: %w", err) // cannot tell new ids from old ones: set nothing
		}
		for _, e := range ents {
			pre[e.ID] = true
		}
	}
	var created []string
	rollback := func() {
		for _, id := range created {
			_ = c.vals.Remove(id, storeActor)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(values)) {
		if err := c.vals.Set(id, values[id], storeActor); err != nil {
			rollback()
			return fmt.Errorf("store value %q: %w", id, err)
		}
		if !pre[id] {
			created = append(created, id)
		}
	}
	if err := c.persistLocked(cand, merged, src, events); err != nil {
		rollback()
		return err
	}
	return nil
}

func action(create bool) string {
	if create {
		return broker.CredCreated
	}
	return broker.CredUpdated
}

// webTokenRules are the rules a web token entry has on top of
// StaticToken.Validate.
func webTokenRules(e config.StaticToken) error {
	if e.Value.Type == "env" {
		return fmt.Errorf("token %q: %w", e.Name, ErrEnvRefOnWeb)
	}
	for _, h := range e.Egress {
		if h.Private || h.CIDR != "" {
			return fmt.Errorf("token %q: %w", e.Name, ErrPrivateEgressOnWeb)
		}
	}
	if e.MCP != nil {
		return fmt.Errorf("token %q: %w", e.Name, ErrMCPOnWeb)
	}
	if e.Template != "" && !tokenTemplateRe.MatchString(e.Template) {
		return fmt.Errorf("token %q: template must be lowercase letters, digits and - (at most 32)", e.Name)
	}
	return nil
}

// PutTokens creates (create) or replaces several static token entries at once,
// all or nothing (a template such as Datadog defines two). values maps an entry
// NAME to its new value; that entry's reference becomes {store, id} with id =
// its existing store id, else its name. The caller owns and zeroes values.
func (c *CredentialManager) PutTokens(entries []config.StaticToken, values map[string][]byte, create bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(entries) == 0 {
		return errors.New("no token entries")
	}
	for name := range values {
		if !slices.ContainsFunc(entries, func(e config.StaticToken) bool { return e.Name == name }) {
			return fmt.Errorf("a value is given for %q, which is not an entry in this request", name)
		}
	}
	cand := c.cloneFile()
	toSet := map[string][]byte{}
	owner := map[string]string{}
	var want []string
	var events []broker.CredEvent
	for i, in := range entries {
		e := in.Clone()
		if slices.ContainsFunc(entries[:i], func(x config.StaticToken) bool { return x.Name == e.Name }) {
			return fmt.Errorf("token %q is listed twice", e.Name)
		}
		if err := c.presence(credfile.KindToken, e.Name, create); err != nil {
			return err
		}
		if v, ok := values[e.Name]; ok {
			if len(v) == 0 {
				return fmt.Errorf("token %q: the value is empty", e.Name)
			}
			id := e.Name
			if e.Value.Type == "store" && e.Value.ID != "" {
				id = e.Value.ID
			}
			if !secretstore.ValidID(id) {
				return fmt.Errorf("token %q: %w", e.Name, secretstore.ErrBadID)
			}
			if _, dup := toSet[id]; dup {
				return fmt.Errorf("token %q: store id %q is used twice in this request", e.Name, id)
			}
			e.Value = config.KeyRef{Type: "store", ID: id}
			toSet[id] = v
			owner[id] = "static_tokens/" + e.Name
		}
		if err := webTokenRules(e); err != nil {
			return err
		}
		if j := c.fileIndex(credfile.KindToken, e.Name); j >= 0 {
			cand.StaticTokens[j] = e
		} else {
			cand.StaticTokens = append(cand.StaticTokens, e)
		}
		want = append(want, credfile.KindToken+"/"+e.Name)
		events = append(events, broker.CredEvent{Action: action(create), Kind: credfile.KindToken, Name: e.Name, Detail: e.Template})
	}
	merged, src, err := c.checkLocked(cand, want, nil)
	if err != nil {
		return err
	}
	return c.setAndPersistLocked(toSet, owner, cand, merged, src, events)
}

// PutGitHubApp creates or replaces a GitHub App. A non-empty keyValue is the
// private key: it goes to the store (id = the App's existing store id, else
// gh-<name>-key) and the entry references it. A replace that names no key and
// gives none keeps the current reference. The caller owns and zeroes keyValue.
func (c *CredentialManager) PutGitHubApp(app config.GitHubApp, keyValue []byte, create bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.presence(credfile.KindGitHubApp, app.Name, create); err != nil {
		return err
	}
	j := c.fileIndex(credfile.KindGitHubApp, app.Name)
	if app.PrivateKey == (config.KeyRef{}) && len(keyValue) == 0 && j >= 0 {
		app.PrivateKey = c.file.GitHubApps[j].PrivateKey
	}
	toSet := map[string][]byte{}
	owner := map[string]string{}
	if len(keyValue) > 0 {
		id := "gh-" + app.Name + "-key"
		if app.PrivateKey.Type == "store" && app.PrivateKey.ID != "" {
			id = app.PrivateKey.ID
		}
		if !secretstore.ValidID(id) {
			return fmt.Errorf("GitHub App %q: the key's store id %q is not valid; name one with private_key.id", app.Name, id)
		}
		app.PrivateKey = config.KeyRef{Type: "store", ID: id}
		toSet[id] = keyValue
		owner[id] = "github_apps/" + app.Name
	}
	if app.PrivateKey.Type == "env" {
		return fmt.Errorf("GitHub App %q: %w", app.Name, ErrEnvRefOnWeb)
	}
	cand := c.cloneFile()
	if j >= 0 {
		cand.GitHubApps[j] = app
	} else {
		cand.GitHubApps = append(cand.GitHubApps, app)
	}
	merged, src, err := c.checkLocked(cand, []string{credfile.KindGitHubApp + "/" + app.Name}, nil)
	if err != nil {
		return err
	}
	ev := broker.CredEvent{Action: action(create), Kind: credfile.KindGitHubApp, Name: app.Name}
	return c.setAndPersistLocked(toSet, owner, cand, merged, src, []broker.CredEvent{ev})
}

// prepAWS is PutAWS's pre-check: presence, the entry's own rules, and whether
// the base role must be (re)checked: on create, and when source_profile or
// role_arn changed. Caller holds c.mu.
func (c *CredentialManager) prepAWS(conn config.AWSConnection, create bool) (needsCheck bool, err error) {
	if err := c.presence(credfile.KindAWS, conn.Name, create); err != nil {
		return false, err
	}
	if err := conn.Validate(); err != nil {
		return false, err
	}
	j := c.fileIndex(credfile.KindAWS, conn.Name)
	if j < 0 {
		return true, nil
	}
	old := c.file.AWSConnections[j]
	return old.SourceProfile != conn.SourceProfile || old.RoleARN != conn.RoleARN, nil
}

// checkRole runs the base-role check, keeps the verdict and audits the call.
// It does not need c.mu held, and takes it only to store the verdict when
// locked is false.
func (c *CredentialManager) checkRole(ctx context.Context, conn config.AWSConnection, locked bool) (awsprobe.Result, error) {
	if c.chk == nil {
		return awsprobe.Result{}, ErrNoBroker
	}
	if wait, ok := c.checkLimit.allow(); !ok {
		return awsprobe.Result{}, ErrCheckLimited{Wait: wait}
	}
	// The row that says the signer is about to be used comes first: a call that
	// cannot be audited does not happen.
	if err := c.rec.RecordCred(broker.CredEvent{Action: broker.CredChecked, Kind: credfile.KindAWS, Name: conn.Name,
		Actor: storeActor, Detail: conn.RoleARN + " started"}); err != nil {
		return awsprobe.Result{}, fmt.Errorf("%w: audit: %w", ErrCredPersist, err)
	}
	res := c.chk.Check(ctx, broker.TargetOf(conn.AWSBinding)).Normalize()
	if !locked {
		c.mu.Lock()
	}
	c.setVerdictLocked(conn.SourceProfile, conn.RoleARN, res)
	if !locked {
		c.mu.Unlock()
	}
	detail := conn.RoleARN + " " + string(res.State)
	if res.Reason != "" {
		detail += " " + string(res.Reason)
	}
	if err := c.rec.RecordCred(broker.CredEvent{Action: broker.CredChecked, Kind: credfile.KindAWS, Name: conn.Name,
		Actor: storeActor, Detail: detail}); err != nil {
		return res, fmt.Errorf("%w: audit: %w", ErrCredPersist, err)
	}
	return res, nil
}

// PutAWS creates or replaces an AWS connection. On create, and on an edit that
// changes source_profile or role_arn, it checks the base role itself (the
// server is the authority, §9.4): a `writes` verdict without ackWrite returns
// ErrNeedsAck and saves nothing; with ackWrite the acknowledgement is audited.
// Other edits do not re-check. The result is the verdict of this call's check,
// or the last known verdict when none ran.
//
// The check (an AWS call, up to its timeout) runs outside the lock; the
// presence and change rules are re-run under the lock before the write, and a
// check is made there if a concurrent save made one necessary.
func (c *CredentialManager) PutAWS(ctx context.Context, conn config.AWSConnection, create, ackWrite bool) (awsprobe.Result, error) {
	conn = conn.Clone()
	c.mu.Lock()
	needs, err := c.prepAWS(conn, create)
	c.mu.Unlock()
	if err != nil {
		return awsprobe.Result{}, err
	}
	var res awsprobe.Result
	checked := false
	if needs {
		if res, err = c.checkRole(ctx, conn, false); err != nil {
			return res, err
		}
		checked = true
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if needs, err = c.prepAWS(conn, create); err != nil {
		return res, err
	}
	if needs && !checked {
		if res, err = c.checkRole(ctx, conn, true); err != nil {
			return res, err
		}
		checked = true
	}
	if !checked {
		res = awsprobe.Lookup(c.verdicts, verdictKey(conn.SourceProfile, conn.RoleARN))
	}
	var events []broker.CredEvent
	if checked && res.State == awsprobe.Writes {
		if !ackWrite {
			return res, ErrNeedsAck{Result: res}
		}
		events = append(events, broker.CredEvent{Action: broker.CredWriteAck, Kind: credfile.KindAWS, Name: conn.Name, Detail: conn.RoleARN})
	}
	events = append(events, broker.CredEvent{Action: action(create), Kind: credfile.KindAWS, Name: conn.Name})

	cand := c.cloneFile()
	if j := c.fileIndex(credfile.KindAWS, conn.Name); j >= 0 {
		cand.AWSConnections[j] = conn
	} else {
		cand.AWSConnections = append(cand.AWSConnections, conn)
	}
	merged, src, err := c.checkLocked(cand, []string{credfile.KindAWS + "/" + conn.Name}, nil)
	if err != nil {
		return res, err
	}
	return res, c.persistLocked(cand, merged, src, events)
}

// Delete removes a web entry. A config.json entry is ErrCredSeed; an entry any
// profile (seed or runtime) binds is ErrCredInUse with their names. The store
// value an entry used is left in place (the values list shows it unreferenced).
func (c *CredentialManager) Delete(kind, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := kindLabel[kind]; !ok {
		return fmt.Errorf("unknown credential kind %q", kind)
	}
	if c.isSeed(kind, name) {
		return fmt.Errorf("%s %q is %w", kindLabel[kind], name, ErrCredSeed)
	}
	j := c.fileIndex(kind, name)
	if j < 0 {
		return fmt.Errorf("%s %q is %w", kindLabel[kind], name, ErrCredNotFound)
	}
	if c.pm != nil {
		if by := c.pm.RefsTo(kind, name); len(by) > 0 {
			return fmt.Errorf("%s %q: %w", kindLabel[kind], name, ErrCredInUse{By: by})
		}
	}
	cand := c.cloneFile()
	switch kind {
	case credfile.KindAWS:
		cand.AWSConnections = slices.Delete(cand.AWSConnections, j, j+1)
	case credfile.KindGitHubApp:
		cand.GitHubApps = slices.Delete(cand.GitHubApps, j, j+1)
	case credfile.KindToken:
		cand.StaticTokens = slices.Delete(cand.StaticTokens, j, j+1)
	}
	merged, src, err := c.checkLocked(cand, nil, []string{kind + "/" + name})
	if err != nil {
		return err
	}
	return c.persistLocked(cand, merged, src, []broker.CredEvent{{Action: broker.CredDeleted, Kind: kind, Name: name}})
}

// SetValue sets or replaces a store value by id. The caller owns and zeroes v.
func (c *CredentialManager) SetValue(id string, v []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(v) == 0 {
		return errors.New("the value is empty")
	}
	return c.vals.Set(id, v, storeActor)
}

// RemoveValue deletes a store value, refused with ErrCredInUse while any
// entry, profile secret or account token_secret reads it.
func (c *CredentialManager) RemoveValue(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if refs := c.storeRefsLocked()[id]; len(refs) > 0 {
		return fmt.Errorf("store value %q: %w", id, ErrCredInUse{By: refs})
	}
	return c.vals.Remove(id, storeActor)
}
