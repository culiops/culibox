package session

import (
	"container/list"
	"sort"
	"sync"
)

// timelinePendingMax bounds how many unanswered tool_use ids one session's
// parser remembers; a transcript that opens 100k tool calls without a result
// evicts the oldest instead of growing.
const timelinePendingMax = 512

// timelineFilesMax bounds Timeline.Files.
const timelineFilesMax = 200

// pendingTool is what a tool_use remembers for its tool_result to finish: where
// its event sits (seq, stable across ring compaction), when it started, and
// enough of the edit to add it to the file totals once its result is ok. None
// of it is ever tool output.
type pendingTool struct {
	id       string
	seq      int64
	ts       int64
	tool     string
	path     string
	add, del int
}

type fileRec struct {
	FileChange
	tick int64
}

// timelineState is one session's incremental parse of its bound transcript.
// mu serialises parse passes and views for the session; it is never held while
// the Manager's own locks are.
type timelineState struct {
	mu sync.Mutex

	path   string // the bound transcript the offset belongs to
	offset int64  // bytes consumed: through the last complete line
	// skip: the next newline-terminated line is the tail of a line we did not
	// start (a mid-file start, or an overlong line still being discarded).
	skip bool

	evs  []TimelineEvent // chronological; compacted at 2×timelineMaxEvents
	base int64           // seq of evs[0]

	pending map[string]*list.Element // tool_use id -> element holding *pendingTool
	order   *list.List               // oldest first

	files map[string]*fileRec
	tick  int64

	toolCalls int
	partial   bool

	clearSeq int64 // seq of a /clear divider still awaiting the new file's first timestamp, else -1
}

func newTimelineState() *timelineState {
	st := &timelineState{}
	st.resetContent()
	return st
}

// resetContent forgets everything parsed (the file shrank), keeping the path.
func (st *timelineState) resetContent() {
	st.offset, st.skip = 0, false
	st.evs, st.base = nil, 0
	st.pending, st.order = map[string]*list.Element{}, list.New()
	st.files, st.tick = map[string]*fileRec{}, 0
	st.toolCalls, st.partial = 0, false
	st.clearSeq = -1
}

// timelineFor returns the session's state, created on first use.
func (m *Manager) timelineFor(id string) *timelineState {
	m.timelineMu.Lock()
	defer m.timelineMu.Unlock()
	if m.timelines == nil {
		m.timelines = map[string]*timelineState{}
	}
	st := m.timelines[id]
	if st == nil {
		st = newTimelineState()
		m.timelines[id] = st
	}
	return st
}

// timelineForget drops a dead session's parse, beside peekForget. Takes only
// timelineMu, never while m.mu is held.
func (m *Manager) timelineForget(id string) {
	m.timelineMu.Lock()
	defer m.timelineMu.Unlock()
	delete(m.timelines, id)
}

// add appends an event and returns its seq. Past timelineMaxEvents the oldest
// fall out of view and partial is set; the slice itself is compacted only at
// twice the cap so an append stays O(1) on a 100k-call transcript.
func (st *timelineState) add(ev TimelineEvent) int64 {
	if st.clearSeq >= 0 && ev.Kind != "clear" {
		// The divider has no time of its own: pin it just before the new
		// file's first event so it sorts where the switch happened.
		if i := st.clearSeq - st.base; i >= 0 && i < int64(len(st.evs)) && ev.TS < st.evs[i].TS {
			st.evs[i].TS = ev.TS
		}
		st.clearSeq = -1
	}
	seq := st.base + int64(len(st.evs))
	st.evs = append(st.evs, ev)
	if len(st.evs) > timelineMaxEvents {
		st.partial = true
	}
	if len(st.evs) >= 2*timelineMaxEvents {
		drop := len(st.evs) - timelineMaxEvents
		st.evs = append(st.evs[:0], st.evs[drop:]...)
		st.base += int64(drop)
	}
	return seq
}

// at is the live event for seq, nil once it fell out of the ring.
func (st *timelineState) at(seq int64) *TimelineEvent {
	i := seq - st.base
	if i < 0 || i >= int64(len(st.evs)) {
		return nil
	}
	return &st.evs[i]
}

func (st *timelineState) addPending(p *pendingTool) {
	if e, ok := st.pending[p.id]; ok {
		st.order.Remove(e)
	}
	st.pending[p.id] = st.order.PushBack(p)
	for st.order.Len() > timelinePendingMax {
		old := st.order.Front()
		delete(st.pending, old.Value.(*pendingTool).id)
		st.order.Remove(old)
	}
}

func (st *timelineState) takePending(id string) *pendingTool {
	e, ok := st.pending[id]
	if !ok {
		return nil
	}
	delete(st.pending, id)
	return st.order.Remove(e).(*pendingTool)
}

// abandonPending turns every still-unanswered call into "state unknown": its
// transcript is no longer being read, so "pending" would be a lie.
func (st *timelineState) abandonPending() {
	for e := st.order.Front(); e != nil; e = e.Next() {
		if ev := st.at(e.Value.(*pendingTool).seq); ev != nil {
			ev.State = ""
		}
	}
	st.pending, st.order = map[string]*list.Element{}, list.New()
}

// touch adds an edit's line counts to path's running total and keeps the
// latest ts (the result's time) it has seen for it.
func (st *timelineState) touch(path string, add, del int, ts int64) {
	st.tick++
	r := st.files[path]
	if r == nil {
		r = &fileRec{FileChange: FileChange{Path: path}}
		st.files[path] = r
		if len(st.files) > timelineFilesMax {
			var oldest string
			var ot int64 = 1 << 62
			for p, o := range st.files {
				if p != path && o.tick < ot {
					oldest, ot = p, o.tick
				}
			}
			delete(st.files, oldest)
		}
	}
	r.Add += add
	r.Del += del
	r.TS = max(r.TS, ts)
	r.tick = st.tick
}

// fileList is the touched files, most recent first.
func (st *timelineState) fileList() []FileChange {
	rs := make([]*fileRec, 0, len(st.files))
	for _, r := range st.files {
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].tick > rs[j].tick })
	out := make([]FileChange, len(rs))
	for i, r := range rs {
		out[i] = r.FileChange
	}
	return out
}

// timelinePath is the transcript the session's parse is bound to ("" if none
// yet). It was proven to be the session's own when it was parsed live.
func (m *Manager) timelinePath(id string) string {
	m.timelineMu.Lock()
	st := m.timelines[id]
	m.timelineMu.Unlock()
	if st == nil {
		return ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.path
}
