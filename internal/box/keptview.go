package box

// The kept-folder stores (keptstore.go) as the profile view and doctor read
// them, and the user's forget.
//
// Everything in a store was written by a box, or, on macOS, can be written by
// the VM, which shares boxstate read-write. So a read here is a read of
// hostile input: state.json is read no-follow with a size cap and cleaned
// (loadKeptState), every path is checked again (validKeptRel), and every
// directory is reached one descriptor at a time and never through a link
// (keptOpenRootPath). os.Root alone is not enough: it follows a link that
// stays inside it when the link is a middle component.
//
// The view reads state.json and nothing under home/: sizes and file counts
// are the ones the keep pass's screen counted. The only directories it lists
// are .kept (to find the stores) and each store's clash/ (to find older
// copies); a kept folder is listed only by the one-directory listing the web
// layer asks for (KeptOpenRoot).

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// View bounds. Variables so tests can lower them.
var (
	keptViewMaxStores     = 256 // stores read per call
	keptViewMaxClashBoxes = 64  // clash/<box> folders looked at per store
)

// keptViewListHook, when set (tests only), runs before every directory the
// view lists, with its path under boxstate/.kept's parent (".kept") or under
// .kept ("<key>/clash").
var keptViewListHook func(rel string)

var (
	// ErrKeptInUse: a box of the store is in use, so its kept folders are
	// mounted and cannot be forgotten now. A forget's refusal also wraps the
	// reason, ErrKeptSessions or ErrKeptStopping. The store is as it was (an
	// idle box removed before ErrKeptStopping stays removed).
	ErrKeptInUse = errors.New("box: a box of this kept store is in use")
	// ErrKeptSessions: a box whose record names the store has a live
	// session. Only the user can end it.
	ErrKeptSessions = errors.New("box: a box of this kept store has a live session")
	// ErrKeptStopping: a box of the store with no session (one that outlives
	// its last session by the idle TTL) could not be removed now.
	ErrKeptStopping = errors.New("box: a box of this kept store could not be stopped")
	// ErrKeptRunUnknown: whether a box of the store runs could not be read
	// (docker did not answer, a record could not be listed). Nothing changed:
	// no box was removed and the store is as it was.
	ErrKeptRunUnknown = errors.New("box: could not tell whether a box of this kept store is running")
	// ErrKeptNotRemoved: the folder is forgotten (not kept, not mounted, not
	// listed any more), but some of its files could not be removed from the
	// store and stay on disk there.
	ErrKeptNotRemoved = errors.New("box: the folder is forgotten, but some of its files could not be removed")
)

// keptAuditForget is the audit action of a forget. detail: path=<p>.
const keptAuditForget = "box_kept_forget"

// KeptStoreView is one store as the profile view and doctor show it, read
// from its state.json only. Profile, Workspace and Account are set only when
// the state hashes to the store's key (keptStateAttribution): a store whose
// state names someone else names nobody. Every path, reason and box name in
// it was chosen by a box: it is data, shown as text, never markup.
type KeptStoreView struct {
	Key       string           `json:"key"`
	Profile   string           `json:"profile"`
	Workspace string           `json:"workspace"`
	Account   string           `json:"account"`
	Kept      []KeptEntry      `json:"kept"`
	NotKept   []KeptRefusalRow `json:"not_kept"`
	// Clashes are the older copies of kept folders, as store-relative folder
	// paths clash/<box>/<rel> that KeptOpenRoot opens. Filled by
	// Manager.KeptStores only.
	Clashes []string `json:"clashes"`
	// TotalBytes is the sum of the kept entries' Bytes (saturating).
	TotalBytes int64 `json:"total_bytes"`
}

// ReadKeptStores reads the state of every store under stateRoot, at most
// keptViewMaxStores of them, in key order. instance is the box instance the
// keys hash with. A store whose state cannot be read is left out; the error
// says which, and that more stores were there than were read. Clashes is
// left empty: it is not state.
func ReadKeptStores(stateRoot, instance string) ([]KeptStoreView, error) {
	state, err := os.OpenRoot(filepath.Join(stateRoot, stateDirName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("box: kept stores: %w", err)
	}
	defer state.Close()
	keys, err := keptStoreKeys(state)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("box: kept stores: %w", err)
	}
	var errs []error
	if len(keys) > keptViewMaxStores {
		errs = append(errs, fmt.Errorf("box: %d kept stores; only the first %d are read", len(keys), keptViewMaxStores))
		keys = keys[:keptViewMaxStores]
	}
	views := make([]KeptStoreView, 0, len(keys))
	for _, key := range keys {
		v, err := readKeptStoreView(state, instance, key)
		if err != nil {
			errs = append(errs, fmt.Errorf("box: kept store %s: %w", key, err))
			continue
		}
		views = append(views, v)
	}
	return views, errors.Join(errs...)
}

// keptStoreKeys lists the store keys in .kept, sorted.
func keptStoreKeys(state *os.Root) ([]string, error) {
	if keptViewListHook != nil {
		keptViewListHook(keptDirName)
	}
	d, _, err := keptOpenPath(state, keptDirName)
	if err != nil {
		return nil, err
	}
	names, err := keptReadNames(d)
	d.Close()
	if err != nil {
		return nil, err
	}
	keys := slices.DeleteFunc(names, func(n string) bool { return !ValidConversationsKey(n) })
	slices.Sort(keys)
	return keys, nil
}

// readKeptStoreView reads store key's state into its view.
func readKeptStoreView(state *os.Root, instance, key string) (KeptStoreView, error) {
	store, err := keptOpenRootPath(state, keptDirName+"/"+key)
	if err != nil {
		return KeptStoreView{}, err
	}
	defer store.Close()
	st, err := loadKeptState(store)
	if err != nil {
		return KeptStoreView{}, err
	}
	return keptView(instance, key, st), nil
}

func keptView(instance, key string, st KeptState) KeptStoreView {
	v := KeptStoreView{Key: key, Kept: st.Kept, NotKept: st.NotKept, Clashes: []string{}}
	if v.Kept == nil {
		v.Kept = []KeptEntry{}
	}
	if v.NotKept == nil {
		v.NotKept = []KeptRefusalRow{}
	}
	if p, w, a, ok := keptStateAttribution(instance, key, st); ok {
		v.Profile, v.Workspace, v.Account = p, w, a
	}
	for _, e := range v.Kept { // cleanKeptState made every Bytes >= 0
		if v.TotalBytes > math.MaxInt64-e.Bytes {
			v.TotalBytes = math.MaxInt64
		} else {
			v.TotalBytes += e.Bytes
		}
	}
	return v
}

// KeptStores is the stores of profile (ReadKeptStores, filtered to the ones
// whose state hashes to their key and names profile), with their clash copies.
// What could not be read is logged and left out.
func (m *Manager) KeptStores(profile string) []KeptStoreView {
	if profile == "" {
		return nil
	}
	views, err := ReadKeptStores(m.opts.StateRoot, m.opts.Instance)
	if err != nil {
		log.Printf("box: reading the kept stores: %v", err)
	}
	var out []KeptStoreView
	for _, v := range views {
		if v.Profile != profile {
			continue
		}
		v.Clashes = keptClashes(m.opts.StateRoot, v.Key, v.Kept)
		out = append(out, v)
	}
	return out
}

// KeptStore is store key's view when it is profile's (as KeptStores decides),
// read from its own state.json alone. Clashes is left empty.
func (m *Manager) KeptStore(profile, key string) (KeptStoreView, bool) {
	if profile == "" || !ValidConversationsKey(key) {
		return KeptStoreView{}, false
	}
	state, err := os.OpenRoot(filepath.Join(m.opts.StateRoot, stateDirName))
	if err != nil {
		return KeptStoreView{}, false
	}
	defer state.Close()
	v, err := readKeptStoreView(state, m.opts.Instance, key)
	if err != nil || v.Profile != profile {
		return KeptStoreView{}, false
	}
	return v, true
}

// keptClashes lists, for each clash/<box> of store key (box names only, at
// most keptViewMaxClashBoxes, in name order), which kept paths have an older
// copy there: a real directory clash/<box>/<rel>, reached without a link. The
// store's clash/ is the only directory listed; nothing below is.
func keptClashes(stateRoot, key string, kept []KeptEntry) []string {
	out := []string{}
	if len(kept) == 0 {
		return out
	}
	store, err := keptOpenStore(stateRoot, key)
	if err != nil {
		return out
	}
	defer store.Close()
	clash, err := keptOpenRootPath(store, keptClashDirName)
	if err != nil {
		return out
	}
	defer clash.Close()
	if keptViewListHook != nil {
		keptViewListHook(key + "/" + keptClashDirName)
	}
	boxes, err := keptBoxDirs(clash)
	if err != nil {
		return out
	}
	if len(boxes) > keptViewMaxClashBoxes {
		boxes = boxes[:keptViewMaxClashBoxes]
	}
	for _, b := range boxes {
		for _, e := range kept {
			d, _, err := keptOpenPath(clash, b+"/"+e.Path)
			if err != nil {
				continue
			}
			d.Close()
			out = append(out, keptClashDirName+"/"+b+"/"+e.Path)
		}
	}
	slices.Sort(out)
	return out
}

// keptBoxDirs lists the names in root that are box names, sorted.
func keptBoxDirs(root *os.Root) ([]string, error) {
	d, err := openDirIn(root, ".")
	if err != nil {
		return nil, err
	}
	names, err := keptReadNames(d)
	d.Close()
	if err != nil {
		return nil, err
	}
	names = slices.DeleteFunc(names, func(n string) bool { return !isBoxName(n) })
	slices.Sort(names)
	return names, nil
}

// KeptSplitPath splits p, a path the profile view names inside a store, into
// the folder KeptOpenRoot opens and the path below it ("." for the folder
// itself). p is <rel>[/<inner>], a kept folder (validKeptRel) and a path in
// it, or clash/<box>/<rel>[/<inner>], an older copy of one. It must be clean
// and local. The kept folder is the shortest leading part of p that is one:
// the four levels make that unique.
func KeptSplitPath(p string) (folder, inner string, ok bool) {
	if p == "" || p == "." || !filepath.IsLocal(p) || path.Clean(p) != p {
		return "", "", false
	}
	parts := strings.Split(p, "/")
	prefix := ""
	if parts[0] == keptClashDirName {
		if len(parts) < 3 || !isBoxName(parts[1]) {
			return "", "", false
		}
		prefix, parts = parts[0]+"/"+parts[1]+"/", parts[2:]
	}
	for n := 1; n <= 3 && n <= len(parts); n++ {
		rel := strings.Join(parts[:n], "/")
		if !validKeptRel(rel) {
			continue
		}
		inner = "."
		if n < len(parts) {
			inner = strings.Join(parts[n:], "/")
		}
		return prefix + rel, inner, true
	}
	return "", "", false
}

// keptFolderRel is the store-relative path of the folder rel names: a kept
// folder (home/<rel>) or an older copy of one (clash/<box>/<rel>).
func keptFolderRel(rel string) (string, bool) {
	if validKeptRel(rel) {
		return keptHomeDirName + "/" + rel, true
	}
	rest, ok := strings.CutPrefix(rel, keptClashDirName+"/")
	if !ok {
		return "", false
	}
	b, krel, ok := strings.Cut(rest, "/")
	if !ok || !isBoxName(b) || !validKeptRel(krel) {
		return "", false
	}
	return rel, true
}

// KeptOpenRoot opens the folder rel of store key as a root: a kept folder
// (rel passes validKeptRel) or an older copy of one (clash/<box>/<rel>). Every
// directory from boxstate down to it is reached one descriptor at a time, a
// real directory and never a link (keptOpenRootPath). The root still follows
// a link inside the folder when a path through it has one in the middle, so
// a caller opens what is below it one descriptor at a time too
// (boxread.OpenIn, boxread.ReadDirIn). fs.ErrNotExist for a key or rel that
// cannot name a folder. The caller closes the root.
func (m *Manager) KeptOpenRoot(key, rel string) (*os.Root, error) {
	sub, ok := keptFolderRel(rel)
	if !ValidConversationsKey(key) || !ok {
		return nil, fs.ErrNotExist
	}
	store, err := keptOpenStore(m.opts.StateRoot, key)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return keptOpenRootPath(store, sub)
}

// keptOpenStore opens store key, never creating it.
func keptOpenStore(stateRoot, key string) (*os.Root, error) {
	if !ValidConversationsKey(key) {
		return nil, fs.ErrNotExist
	}
	state, err := os.OpenRoot(filepath.Join(stateRoot, stateDirName))
	if err != nil {
		return nil, err
	}
	defer state.Close()
	return keptOpenRootPath(state, keptDirName+"/"+key)
}

// keptOpenRootPath opens the directory rel (clean, local, not ".") under root
// as a root of its own, one component at a time: each is checked with
// keptOpenPath (fstatat no-follow on its parent's descriptor: a real
// directory, never a link), opened as a root of that one name, and checked to
// be the directory just seen (keptRootIs). A name with more than one
// component is never handed to os.Root, which would follow a link in its
// middle. A link is errKeptLink, a non-directory errKeptNotDir, a missing
// component fs.ErrNotExist.
func keptOpenRootPath(root *os.Root, rel string) (*os.Root, error) {
	if rel == "" || rel == "." || !filepath.IsLocal(rel) || filepath.Clean(rel) != rel {
		return nil, fmt.Errorf("box: %q is not a clean relative path", rel)
	}
	cur := root
	for _, c := range strings.Split(rel, "/") {
		d, st, err := keptOpenPath(cur, c)
		if err != nil {
			if cur != root {
				cur.Close()
			}
			return nil, err
		}
		d.Close()
		next, err := cur.OpenRoot(c)
		if cur != root {
			cur.Close()
		}
		if err != nil {
			return nil, err
		}
		if !keptRootIs(next, st) {
			next.Close()
			return nil, fmt.Errorf("box: %s: %w", keptShow(rel), errKeptChanged)
		}
		cur = next
	}
	return cur, nil
}

// lockWithin takes mu, or returns ctx's error if ctx ends first. An abandoned
// wait still takes mu when it frees up, and gives it straight back (as
// lockOp does for opMu).
func lockWithin(ctx context.Context, mu *sync.Mutex) error {
	if mu.TryLock() {
		return nil
	}
	got := make(chan struct{})
	go func() {
		mu.Lock()
		close(got)
	}()
	select {
	case <-got:
		return nil
	case <-ctx.Done():
		go func() {
			<-got
			mu.Unlock()
		}()
		return ctx.Err()
	}
}

// KeptForget forgets the kept folder rel of store key, on the word of actor
// (the person, as the web layer names them: "web", "bearer"): its home/<rel>
// and every older copy of it (keptRemoveClashes) are removed, its entry and not-kept rows go, and rel is added to the
// store's forgotten list, so no keep pass keeps it again. The audit row
// box_kept_forget names actor and the store's profile
// (keptStateAttribution), else "unknown".
//
// It takes the store's lock first and opMu after it, the keep pass's order.
// Under opMu, the boxes whose record names the store are cleared
// (keptClearBoxes): one with a live session refuses the forget
// (ErrKeptSessions), one that only outlives its last session is removed as a
// spawn removes an idle box in its way (ErrKeptStopping when it will not go),
// and then the state is saved. So no box mounts rel from then on: none of the
// store's runs, and one created later reads the saved state (ensure reads
// state.json under opMu). A removed box's keep pass runs in its own goroutine
// (stopped) and waits for the store's lock, so it starts once this forget is
// done and finds rel forgotten. The removal of the files runs after opMu is
// let go, under the store's lock only: a large folder must not hold every
// spawn. When some of it cannot be removed the error is ErrKeptNotRemoved:
// rel is forgotten all the same, and a forget of rel again tries the rest
// while anything is left at home/<rel>.
//
// fs.ErrNotExist when key or rel cannot name a kept folder, or the store has
// neither an entry nor a folder for rel. ErrKeptRunUnknown when whether a box
// runs could not be read. A state that cannot be read is never written over.
func (m *Manager) KeptForget(ctx context.Context, key, rel, actor string) error {
	if !ValidConversationsKey(key) || !validKeptRel(rel) {
		return fs.ErrNotExist
	}
	mu := m.keptStoreLock(key)
	if err := lockWithin(ctx, mu); err != nil {
		return err
	}
	defer mu.Unlock()
	store, err := keptOpenStore(m.opts.StateRoot, key)
	if err != nil {
		return err
	}
	defer store.Close()
	st, err := loadKeptState(store)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(st.Kept, func(e KeptEntry) bool { return e.Path == rel }) && !keptPresent(store, rel) {
		return fs.ErrNotExist
	}
	next := keptCopy(st)
	next.Kept = slices.DeleteFunc(next.Kept, func(e KeptEntry) bool { return e.Path == rel })
	next.NotKept = slices.DeleteFunc(next.NotKept, func(r KeptRefusalRow) bool { return strings.EqualFold(r.Path, rel) })
	if !keptFoldIn(rel, next.Forgotten) {
		next.Forgotten = append(next.Forgotten, rel)
	}

	err = func() error {
		if err := m.lockOp(ctx); err != nil {
			return err
		}
		defer m.opMu.Unlock()
		if err := m.keptClearBoxes(ctx, key); err != nil {
			return err
		}
		return saveKeptState(store, next)
	}()
	if err != nil {
		return err
	}
	m.keptForgetAudit(key, next, rel, actor)
	log.Printf("box: kept store %s: forgot %q on the user's word; it is not kept again", key, rel)
	if err := keptRemoveCopies(ctx, store, rel); err != nil {
		log.Printf("box: kept store %s: %q is forgotten, but some of its files stay in the store: %v", key, rel, err)
		return fmt.Errorf("%w: %v", ErrKeptNotRemoved, err)
	}
	return nil
}

// keptPresent reports whether the store's home/ has anything at rel, reached
// without a link (a link there counts, as itself).
func keptPresent(store *os.Root, rel string) bool {
	dir := keptHomeDirName
	if d := path.Dir(rel); d != "." {
		dir += "/" + d
	}
	parent, err := keptOpenRootPath(store, dir)
	if err != nil {
		return false
	}
	defer parent.Close()
	_, err = parent.Lstat(path.Base(rel)) // one name: nothing to follow
	return err == nil
}

// keptClearBoxes makes sure no box of store key runs, for a forget. The
// store's boxes are those whose record names key, and those whose record
// cannot be read (they may be). If any has a live session it returns
// ErrKeptSessions and touches nothing. Otherwise each one still running (it
// outlives its last session by the idle TTL) is removed with removeIdle, as a
// spawn removes an idle box in its way; one that will not go is
// ErrKeptStopping. ErrKeptRunUnknown when the records or docker cannot say,
// with nothing removed: every box is inspected before any is.
//
// A record outlives its box (an elevatable profile makes a new box every
// session), so docker is asked once which boxes exist, and only those are
// inspected: a record whose box is not listed and has no session is skipped
// without reading it. The caller holds opMu: no session starts in a box
// meanwhile (ensure holds opMu when it counts one), and no box is created.
func (m *Manager) keptClearBoxes(ctx context.Context, key string) error {
	state, err := os.OpenRoot(filepath.Join(m.opts.StateRoot, stateDirName))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrKeptRunUnknown, err)
	}
	defer state.Close()
	d, _, err := keptOpenPath(state, keptDirName+"/"+keptBoxesDirName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // no box was ever created with a store
	}
	if err != nil {
		return fmt.Errorf("%w: the box records: %v", ErrKeptRunUnknown, err)
	}
	names, err := keptReadNames(d)
	d.Close()
	if err != nil {
		return fmt.Errorf("%w: the box records: %v", ErrKeptRunUnknown, err)
	}
	names = slices.DeleteFunc(names, func(n string) bool { return !isBoxName(n) }) // a record's temp file
	if len(names) == 0 {
		return nil
	}
	listed, err := m.opts.Client.ListBoxes(ctx, LabelInstance+"="+m.opts.Instance)
	if err != nil {
		return fmt.Errorf("%w: listing the boxes: %v", ErrKeptRunUnknown, err)
	}
	exists := make(map[string]bool, len(listed))
	for _, b := range listed {
		exists[b.Name] = true
	}
	slices.Sort(names)
	var boxes []string
	for _, name := range names {
		if !exists[name] && m.idle(name) {
			continue // no container and no session: nothing of it can mount the store
		}
		if rec, ok := readKeptBoxRecord(m.opts.StateRoot, name); ok && rec.Key != key {
			continue
		}
		if !m.idle(name) {
			return fmt.Errorf("%w: %w: %s", ErrKeptInUse, ErrKeptSessions, name)
		}
		boxes = append(boxes, name)
	}
	var running []string
	for _, name := range boxes {
		st, err := m.opts.Client.Inspect(ctx, name)
		if err != nil {
			return fmt.Errorf("%w: %s: %v", ErrKeptRunUnknown, name, err)
		}
		if st == StateRunning {
			running = append(running, name)
		}
	}
	for _, name := range running {
		if err := m.removeIdle(ctx, name); err != nil {
			log.Printf("box: %s: not removed for a forget of a kept folder: %v", name, err)
			return fmt.Errorf("%w: %w: %s: %v", ErrKeptInUse, ErrKeptStopping, name, err)
		}
		log.Printf("box: %s: removed, idle, so a kept folder of its profile can be forgotten", name)
	}
	return nil
}

// keptForgetAudit writes the forget's audit row, as actor's. A failure is
// logged: the forget has happened either way.
func (m *Manager) keptForgetAudit(key string, st KeptState, rel, actor string) {
	if m.opts.Audit == nil {
		return
	}
	name, _, _, ok := keptStateAttribution(m.opts.Instance, key, st)
	if !ok {
		name = "unknown"
	}
	if actor == "" {
		actor = "unknown"
	}
	if err := m.opts.Audit(actor, keptAuditForget, name, "path="+rel); err != nil {
		log.Printf("box: kept store %s: audit row %s not written: %v", key, keptAuditForget, err)
	}
}

// keptRemoveCopies removes home/<rel> and every older copy of rel
// (keptRemoveClashes).
func keptRemoveCopies(ctx context.Context, store *os.Root, rel string) error {
	_, herr := keptRemoveIn(ctx, store, keptHomeDirName+"/"+rel, false)
	_, cerr := keptRemoveClashes(ctx, store, rel)
	return errors.Join(herr, cerr)
}

// keptRemoveClashes removes the older copies of rel in every clash/<box> of
// the store (a box name, reached without a link): <rel>, the numbered
// <rel>~N beside it, and the same under each clash/<box>/~N, where toClash
// puts a copy that cannot go under its own path. n is how many it removed.
func keptRemoveClashes(ctx context.Context, store *os.Root, rel string) (n int, err error) {
	clash, err := keptOpenRootPath(store, keptClashDirName)
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, errKeptLink) || errors.Is(err, errKeptNotDir):
		return 0, nil // none of the store's own there: a link is never followed
	case err != nil:
		return 0, err
	}
	defer clash.Close()
	boxes, err := keptBoxDirs(clash)
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, b := range boxes {
		dirs := []string{b}
		if sub, err := keptOpenRootPath(clash, b); err == nil {
			names, err := keptAllNames(sub)
			sub.Close()
			if err != nil {
				errs = append(errs, err)
			}
			for _, nm := range names {
				if keptFallbackDir(nm) {
					dirs = append(dirs, b+"/"+nm)
				}
			}
		}
		for _, d := range dirs {
			k, err := keptRemoveIn(ctx, clash, d+"/"+rel, true)
			n += k
			if err != nil {
				errs = append(errs, err)
			}
		}
	}
	return n, errors.Join(errs...)
}

// keptFallbackDir reports whether n is ~N for N in 1..keptClashTries, a folder
// toClash falls back to.
func keptFallbackDir(n string) bool {
	k, ok := strings.CutPrefix(n, "~")
	if !ok {
		return false
	}
	i, err := strconv.Atoi(k)
	return err == nil && i >= 1 && i <= keptClashTries && strconv.Itoa(i) == k
}

// keptRemoveIn removes rel (two components or more) under root: its parent is
// reached by keptOpenRootPath, so a link or a non-directory on the way means
// there is nothing of the store's to remove. numbered also removes the
// numbered clash copies <name>~2 .. <name>~keptClashTries beside it. n is how
// many names it removed.
func keptRemoveIn(ctx context.Context, root *os.Root, rel string, numbered bool) (n int, err error) {
	parent, err := keptOpenRootPath(root, path.Dir(rel))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errKeptLink) || errors.Is(err, errKeptNotDir) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer parent.Close()
	base := path.Base(rel)
	var names []string
	if _, err := parent.Lstat(base); !errors.Is(err, fs.ErrNotExist) { // one name: nothing to follow
		names = append(names, base)
	}
	if numbered {
		all, err := keptAllNames(parent)
		if err != nil {
			return 0, err
		}
		for _, nm := range all {
			if keptNumbered(nm, base) {
				names = append(names, nm)
			}
		}
	}
	var errs []error
	for _, nm := range names {
		if err := keptRemoveOne(ctx, parent, nm); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s", strconv.Quote(path.Join(path.Dir(rel), nm)), removalCause(err)))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

// keptAllNames lists root's own directory.
func keptAllNames(root *os.Root) ([]string, error) {
	d, err := openDirIn(root, ".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return keptReadNames(d)
}

// keptNumbered reports whether n is base~k for k in 2..keptClashTries, as
// toClash names a later copy.
func keptNumbered(n, base string) bool {
	k, ok := strings.CutPrefix(n, base+"~")
	if !ok {
		return false
	}
	i, err := strconv.Atoi(k)
	return err == nil && i >= 2 && i <= keptClashTries && strconv.Itoa(i) == k
}

// keptRemoveOne removes the entry name of parent and all below it.
// os.Root.RemoveAll of a single name resolves nothing: it unlinks the name
// from parent's own descriptor, and below it walks by descriptor, opening
// each directory with O_NOFOLLOW|O_DIRECTORY and unlinking a link as the link
// (Go's removeAllFrom), so it never follows a link inside the tree. A tree it
// cannot empty (directories without write permission, a Go module cache) is
// made removable and removed again, as reclaim does (makeRemovable). That walk
// is through a root on the folder itself, so the most a link swapped in can
// redirect it to is another directory of the same doomed folder.
func keptRemoveOne(ctx context.Context, parent *os.Root, name string) error {
	err := parent.RemoveAll(name)
	if err == nil {
		return nil
	}
	sub, serr := keptOpenRootPath(parent, name)
	if serr != nil {
		return err
	}
	makeRemovable(ctx, sub, ".")
	sub.Close()
	return parent.RemoveAll(name)
}

// keptNewsWait is how long a spawn waits for a store's lock to tell what is
// new: a forget holds it through a big removal, and a spawn must not stall
// behind that.
var keptNewsWait = 2 * time.Second

// TakeKeptNews returns the paths kept in store key that no spawn has told the
// person about yet, and marks them told. It takes the store's lock like the
// other writers of state.json, and never opMu (the lock order is store lock
// first). It answers none, and marks nothing, for a bad key, an unknown or
// unreadable store, a store busy for keptNewsWait, or a state that cannot be
// saved: the news then waits for the next spawn.
func (m *Manager) TakeKeptNews(key string) []string {
	if !ValidConversationsKey(key) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), keptNewsWait)
	defer cancel()
	mu := m.keptStoreLock(key)
	if err := lockWithin(ctx, mu); err != nil {
		return nil
	}
	defer mu.Unlock()
	store, err := keptOpenStore(m.opts.StateRoot, key)
	if err != nil {
		return nil
	}
	defer store.Close()
	st, err := loadKeptState(store)
	if err != nil {
		return nil
	}
	next := keptCopy(st)
	var news []string
	for i := range next.Kept {
		if !next.Kept[i].Announced {
			next.Kept[i].Announced = true
			news = append(news, next.Kept[i].Path)
		}
	}
	if len(news) == 0 {
		return nil
	}
	if err := saveKeptState(store, next); err != nil {
		log.Printf("box: kept store %s: marking the news told: %v", key, err)
		return nil
	}
	return news
}

// KeptRoot is the folder holding every kept-folder store.
func KeptRoot(stateRoot string) string {
	return filepath.Join(stateRoot, stateDirName, keptDirName)
}
