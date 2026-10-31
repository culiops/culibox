package clone

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"syscall"
)

// The import record is the host's own memory of which commits a box brought
// into a clone_of that clone_of did not have before. `clone fetch` needs it to
// tell a box's earlier commits (on a growing box/<branch>, or left after the
// user deleted it) from host history the host has since rewritten away. Like
// the base record it lives in the host data dir, reached only through
// os.Root, so nothing a box writes can forge it.
const (
	importRecordDir = "clone-imports" // <dataDir>/clone-imports/<sha256(resolved clone_of)>, 0600, dir 0700
	// maxImportLines bounds the record; the oldest are dropped. A dropped SHA can
	// only cause a refusal (the check fails closed).
	maxImportLines = 200_000
	// maxImportLine is a SHA-256 hex plus the newline, with room for a CR.
	maxImportLine = 80
)

// ReadImportRecord returns the commits earlier fetches brought into of that of
// did not have before (box-made). exists is false when no record exists yet.
func ReadImportRecord(dataDir, of string) (shas map[string]bool, exists bool, err error) {
	name, err := cloneKeyHash(of)
	if err != nil {
		return nil, false, err
	}
	root, err := os.OpenRoot(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	dir, err := root.OpenRoot(importRecordDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	list, exists, err := readImportList(dir, name)
	if err != nil || !exists {
		return nil, false, err
	}
	shas = make(map[string]bool, len(list))
	for _, s := range list {
		shas[s] = true
	}
	return shas, true, nil
}

// AppendImported adds shas to the record of of, creating it when absent. Each
// must be a commit SHA. The record keeps the newest maxImportLines.
func AppendImported(dataDir, of string, shas []string) error {
	for _, s := range shas {
		if !shaRE.MatchString(s) {
			return fmt.Errorf("import record: %q is not a commit SHA", sanitize(s))
		}
	}
	if len(shas) == 0 {
		return nil
	}
	name, err := cloneKeyHash(of)
	if err != nil {
		return err
	}
	dir, unlock, err := lockImports(dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	list, _, err := readImportList(dir, name)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(list))
	for _, s := range list {
		have[s] = true
	}
	added := false
	for _, s := range shas {
		if !have[s] {
			have[s] = true
			list = append(list, s)
			added = true
		}
	}
	if !added {
		return nil
	}
	if len(list) > maxImportLines {
		list = list[len(list)-maxImportLines:]
	}
	return writeImportList(dir, name, list)
}

// seedImportRecord runs once per of, when no record exists: it records the
// commits reachable only from refs/heads/box/*, so a box/* branch fetched before
// this guard does not refuse its own next return. Those commits are already
// reachable in the user's repo; seeding them exposes nothing new.
func seedImportRecord(ctx context.Context, g Git, dataDir, of string) error {
	name, err := cloneKeyHash(of)
	if err != nil {
		return err
	}
	dir, unlock, err := lockImports(dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	if _, exists, err := readImportList(dir, name); err != nil || exists {
		return err
	}
	out, err := g.Run(ctx, of, noReplaceEnv(), hardened("rev-list", "--branches=box/*", "--not",
		"--exclude=refs/agent-box/*", "--exclude=refs/heads/box/*", "--all")...)
	if err != nil {
		return err
	}
	var list []string
	for _, l := range strings.Fields(string(out)) {
		if !shaRE.MatchString(l) {
			return fmt.Errorf("rev-list: unexpected line %q", sanitize(l))
		}
		list = append(list, l)
	}
	slices.Reverse(list) // oldest first, so the cap drops the oldest
	if len(list) > maxImportLines {
		list = list[len(list)-maxImportLines:]
	}
	return writeImportList(dir, name, list)
}

// lockImports opens <dataDir>/clone-imports as a Root and takes an exclusive
// lock for a read-modify-write: the daemon and the CLI are separate processes.
// Closing the lock file drops it.
func lockImports(dataDir string) (*os.Root, func(), error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	if err := root.MkdirAll(importRecordDir, 0o700); err != nil {
		return nil, nil, err
	}
	dir, err := root.OpenRoot(importRecordDir)
	if err != nil {
		return nil, nil, err
	}
	f, err := dir.OpenFile(".lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		dir.Close()
		return nil, nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		dir.Close()
		return nil, nil, err
	}
	return dir, func() { f.Close(); dir.Close() }, nil
}

// readImportList streams the record, oldest first. A symlink, a non-regular
// file, a line that is not a SHA or more than maxImportLines lines is an error.
func readImportList(dir *os.Root, name string) ([]string, bool, error) {
	fi, err := dir.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("import record %s is not a regular file", name)
	}
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, maxImportLine), maxImportLine)
	list := []string{}
	for sc.Scan() {
		line := sc.Text()
		if !shaRE.MatchString(line) {
			return nil, false, fmt.Errorf("import record %s: line %d is not a commit SHA", name, len(list)+1)
		}
		if len(list) == maxImportLines {
			return nil, false, fmt.Errorf("import record %s has more than %d lines", name, maxImportLines)
		}
		list = append(list, line)
	}
	if err := sc.Err(); err != nil {
		return nil, false, fmt.Errorf("import record %s: %w", name, err)
	}
	return list, true, nil
}

// writeImportList writes through a temp file in the same dir and renames it over
// name (the rename replaces, never follows, a symlink at name).
func writeImportList(dir *os.Root, name string, list []string) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmpName := name + ".tmp-" + hex.EncodeToString(rnd[:])
	tmp, err := dir.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			dir.Remove(tmpName)
		}
	}()
	w := bufio.NewWriter(tmp)
	for _, s := range list {
		w.WriteString(s)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := dir.Rename(tmpName, name); err != nil {
		return err
	}
	ok = true
	return nil
}

// noReplaceEnv is CleanEnv plus GIT_NO_REPLACE_OBJECTS: a refs/replace/ entry in
// the user's repo must not change which commit these checks see.
func noReplaceEnv() []string { return append(CleanEnv(), "GIT_NO_REPLACE_OBJECTS=1") }

// missingBefore returns the objects named in the pack index at idx that of lacks
// (show-index | cat-file --batch-check). It must run before the import moves
// any object into of. A thin pack's completed bases are in the index too, but of
// has them, so they are not in the result.
func missingBefore(ctx context.Context, g Git, of, idx string) (map[string]bool, error) {
	sg, ok := g.(StdinGit)
	if !ok {
		return nil, errors.New("this git runner cannot take stdin")
	}
	f, err := os.Open(idx)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out, err := sg.RunStdin(ctx, of, noReplaceEnv(), f, hardened("show-index")...)
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		fl := strings.Fields(line) // <offset> <sha> [(<crc>)]
		if len(fl) < 2 || !shaRE.MatchString(fl[1]) {
			return nil, fmt.Errorf("show-index: unexpected line %q", sanitize(line))
		}
		shas = append(shas, fl[1])
	}
	types, err := objectTypes(ctx, sg, of, shas)
	if err != nil {
		return nil, err
	}
	missing := map[string]bool{}
	for _, s := range shas {
		if types[s] == "missing" {
			missing[s] = true
		}
	}
	return missing, nil
}

// objectTypes asks of for the type of each SHA: "commit", "blob"..., or "missing".
func objectTypes(ctx context.Context, sg StdinGit, of string, shas []string) (map[string]string, error) {
	types := make(map[string]string, len(shas))
	if len(shas) == 0 {
		return types, nil
	}
	out, err := sg.RunStdin(ctx, of, noReplaceEnv(), strings.NewReader(strings.Join(shas, "\n")+"\n"), hardened("cat-file", "--batch-check")...)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(shas) {
		return nil, fmt.Errorf("cat-file --batch-check: %d answers for %d SHAs", len(lines), len(shas))
	}
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != shas[i] {
			return nil, fmt.Errorf("cat-file --batch-check: unexpected line %q", sanitize(line))
		}
		types[shas[i]] = f[1]
	}
	return types, nil
}

// onlyCommits is the SHAs of set that are commits in of, sorted.
func onlyCommits(ctx context.Context, g Git, of string, set map[string]bool) ([]string, error) {
	sg, ok := g.(StdinGit)
	if !ok {
		return nil, errors.New("this git runner cannot take stdin")
	}
	shas := make([]string, 0, len(set))
	for s := range set {
		shas = append(shas, s)
	}
	slices.Sort(shas)
	types, err := objectTypes(ctx, sg, of, shas)
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, s := range shas {
		if types[s] == "commit" {
			commits = append(commits, s)
		}
	}
	return commits, nil
}

// rewrittenAway returns the first commit of tip's history that (a) no ref outside
// refs/agent-box/ and refs/heads/box/ reaches, (b) of had before this import (not
// in missing), and (c) is not in boxMade: a commit the host had that nothing there
// holds any more. Else it returns the first such commit (a) whose SHA is an <old>
// with a different <new> in <of>/.git/filter-repo/commit-map (filter-repo prunes
// at once, so (b) cannot fire for it). Else "". tip's whole history is walked, not
// the pack: a bundle can name a commit as a prerequisite without carrying it.
func rewrittenAway(ctx context.Context, g Git, of, tip string, missing, boxMade map[string]bool) (string, error) {
	sg, ok := g.(StdinGit)
	if !ok {
		return "", errors.New("this git runner cannot take stdin")
	}
	if !shaRE.MatchString(tip) {
		return "", fmt.Errorf("tip %q is not a commit SHA", sanitize(tip))
	}
	out, err := sg.RunStdin(ctx, of, noReplaceEnv(), strings.NewReader(tip+"\n"), hardened("rev-list", "--topo-order", "--stdin",
		"--not", "--exclude=refs/agent-box/*", "--exclude=refs/heads/box/*", "--all")...)
	if err != nil {
		return "", err
	}
	cands := strings.Fields(string(out))
	for _, s := range cands {
		if !shaRE.MatchString(s) {
			return "", fmt.Errorf("rev-list: unexpected line %q", sanitize(s))
		}
	}
	for _, s := range cands {
		if !missing[s] && !boxMade[s] {
			return s, nil
		}
	}
	if len(cands) == 0 {
		return "", nil
	}
	rewritten, err := commitMapRewritten(ctx, of, cands)
	if err != nil {
		return "", err
	}
	for _, s := range cands {
		if rewritten[s] {
			return s, nil
		}
	}
	return "", nil
}

// filterRepoMap is where git-filter-repo leaves its old-to-new commit map.
const filterRepoMap = ".git/filter-repo/commit-map"

// commitMapRewritten returns which of cands filter-repo's commit-map lists as an
// <old> with a different <new> (a rewritten or dropped commit). The map is in
// the user's repo, so it is opened through an os.Root on of without following a
// symlink, and streamed: a large history has one line per commit.
func commitMapRewritten(ctx context.Context, of string, cands []string) (map[string]bool, error) {
	want := make(map[string]bool, len(cands))
	for _, s := range cands {
		want[s] = true
	}
	root, err := os.OpenRoot(of)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	fi, err := root.Lstat(filterRepoMap)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filterRepoMap)
	}
	f, err := root.OpenFile(filterRepoMap, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, maxImportLine), maxImportLine*2+4)
	hit := map[string]bool{}
	for n := 1; sc.Scan(); n++ {
		if n%4096 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		fl := strings.Fields(sc.Text())
		if n == 1 && len(fl) == 2 && fl[0] == "old" && fl[1] == "new" {
			continue
		}
		if len(fl) != 2 || !shaRE.MatchString(fl[0]) || !shaRE.MatchString(fl[1]) {
			return nil, fmt.Errorf("%s: line %d is not \"<old> <new>\"", filterRepoMap, n)
		}
		if want[fl[0]] && fl[0] != fl[1] {
			hit[fl[0]] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", filterRepoMap, err)
	}
	return hit, nil
}
