package box

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// conversationsRel is where Claude Code keeps its conversations in a home: one
// folder per workspace, one <session>.jsonl per conversation directly in it.
// Those files are what `claude --resume` lists, so they are what makes a home
// worth keeping (#330).
const conversationsRel = ".claude/projects"

// readDirChunk is how many entries holdsConversations reads at a time. It stops
// at the first conversation, so a folder the agent filled with other files costs
// only until then, or one full read when there is none.
const readDirChunk = 256

// maxConversationEntries caps how many directory entries one check reads, across
// the projects dir and its workspace folders. The check runs on every pass for
// every kept home, forever, over folders the agent fills: unbounded, a home with
// millions of entries would cost seconds per pass and hold up the homes after
// it. Claude Code makes one folder per workspace and one file per conversation,
// so a real home is far below this. Past the cap the home is kept, unread (a
// variable so a test can lower it).
var maxConversationEntries = 10000

// holdsConversations reports whether the home at dir holds at least one
// conversation: a regular *.jsonl file in a real directory directly below
// conversationsRel. Everything goes through an os.Root on the home and nothing is
// followed: the entry types come from the directory listing, so a symlink (to a
// file, a folder or the projects dir itself) is never taken for the real thing.
//
// When the answer cannot be read (ctx done, a folder that will not open, more
// than maxConversationEntries entries) it is true: the caller deletes the home
// on false, and a deletion cannot be undone.
func holdsConversations(ctx context.Context, dir string) bool {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	defer root.Close()
	if !plainPath(root, conversationsRel) {
		return false
	}
	found := false
	read := 0
	visit := func(match func(fs.DirEntry) bool) func(fs.DirEntry) bool {
		return func(e fs.DirEntry) bool {
			if read++; read > maxConversationEntries {
				found = true // past the cap: keep, unread
				return false
			}
			found = match(e)
			return !found
		}
	}
	err = eachEntry(ctx, root, conversationsRel, visit(func(ws fs.DirEntry) bool {
		if !ws.IsDir() {
			return false
		}
		err := eachEntry(ctx, root, conversationsRel+"/"+ws.Name(), visit(func(f fs.DirEntry) bool {
			return f.Type().IsRegular() && strings.HasSuffix(f.Name(), ".jsonl")
		}))
		// Unreadable keeps the home; a cut short by the cap has set found.
		return found || err != nil
	}))
	return found || err != nil
}

// eachEntry calls visit for every entry of the directory rel under root, in
// chunks, until visit returns false. It stops with ctx's error when ctx is done.
func eachEntry(ctx context.Context, root *os.Root, rel string, visit func(fs.DirEntry) bool) error {
	f, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ents, err := f.ReadDir(readDirChunk)
		for _, e := range ents {
			if !visit(e) {
				return nil
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// KeptHome is a box home that reclaim keeps past the state TTL because it holds
// a conversation.
type KeptHome struct {
	Box string
	Dir string // the home on the host
	// OutDir is the box's out dir (screenshots), beside the home: removing the
	// home by hand should remove it too.
	OutDir string
	Age    time.Duration
	// Bytes is the home's size; Sized is false when the walk did not finish.
	Bytes int64
	Sized bool
}

// KeptHomes lists the box homes under stateRoot that are unused for at least
// ttl and hold a conversation: the ones ReclaimState keeps rather than removes.
// It is read-only and needs no docker, for doctor. It does not ask whether a
// container still uses a home; the last-use marker is touched by every spawn, so
// a home this old is very rarely in use.
//
// The sizes are a full walk of each listed home, bounded by ctx: when ctx is done
// the list so far comes back with ctx's error, and a home whose walk was cut
// short is not Sized.
func KeptHomes(ctx context.Context, stateRoot string, ttl time.Duration) ([]KeptHome, error) {
	dir := filepath.Join(stateRoot, stateDirName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("box: reading %q: %w", dir, err)
	}
	var out []KeptHome
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		name := e.Name()
		if !e.IsDir() || !isBoxName(name) {
			continue
		}
		age, ok := homeAgeAt(stateRoot, name)
		if !ok || age < ttl {
			continue
		}
		home := filepath.Join(dir, name)
		if !holdsConversations(ctx, home) {
			continue
		}
		h := KeptHome{Box: name, Dir: home, OutDir: filepath.Dir(OutputDir(stateRoot, name)), Age: age}
		h.Bytes, err = dirBytesCtx(ctx, home)
		h.Sized = err == nil
		out = append(out, h)
	}
	return out, ctx.Err()
}
