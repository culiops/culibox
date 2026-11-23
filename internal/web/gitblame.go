package web

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/culiops/culibox/internal/gitview"
)

// msgBlameSlow is the reason of a blame that hit the runner's timeout; the
// client shows it and does not retry on its own.
const msgBlameSlow = "Blame took too long for this file"

// blameReply is the body of POST /api/git/blame: the runner's answer, the
// sha256 of the file it describes (the editor's read version, so the client can
// tell whether it matches the text it holds) and the link base of the origin
// remote when there is one.
type blameReply struct {
	gitview.Blame
	Version string       `json:"version,omitempty"`
	Remote  *BlameRemote `json:"remote,omitempty"`
}

// handleGitBlame is POST /api/git/blame {"path": abs file}: the line blame and
// the last commit of one file, for the editor. The file is resolved as for diff
// (gitTarget, gitFileRel), never as for show: a file that is gone has no blame.
// An untracked file, or any in a repository with no commit, is "untracked"
// without a git run; a file over the editor's 2 MiB text cap or a binary one is
// "toobig" without one. Otherwise the runner runs log and blame in the git
// container (gitview.Runner.Blame), keyed by the HEAD of a (reused) status and
// the file's sha256, and the file is hashed again afterwards: a file that changed
// meanwhile is "changed". Every reason a client sees is a fixed message.
func (s *Server) handleGitBlame(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeGitBody(w, r)
	if !ok {
		return
	}
	if s.Git == nil {
		writeJSON(w, http.StatusOK, blameUnavailable(msgGitNoRuntime))
		return
	}
	t, abs, err := s.gitTarget(body.Path)
	switch {
	case errors.Is(err, errGitNotRepo):
		writeJSON(w, http.StatusOK, blameReply{Blame: gitview.Blame{State: "notrepo"}})
		return
	case isGitRefusal(err):
		writeJSON(w, http.StatusOK, blameUnavailable(targetReason(err)))
		return
	case err != nil:
		writeFsErr(w, err)
		return
	}
	rel, err := gitFileRel(t, abs)
	switch {
	case errors.Is(err, errGitNotFile):
		writeErr(w, http.StatusBadRequest, msgGitNotFile)
		return
	case errors.Is(err, errGitOutside):
		writeJSON(w, http.StatusOK, blameUnavailable(msgGitOutside))
		return
	case err != nil:
		writeFsErr(w, err)
		return
	}
	p, err := s.fsPolicy()
	if err != nil {
		writeFsErr(w, err)
		return
	}
	version, text, err := p.blameVersion(abs)
	switch {
	case err != nil:
		writeFsErr(w, err)
		return
	case !text:
		writeJSON(w, http.StatusOK, blameReply{Blame: gitview.Blame{State: "toobig"}})
		return
	}

	st := s.Git.Status(r.Context(), t)
	switch {
	case st.State == "ok" && (st.NoHead || untrackedIn(st, rel)):
		writeJSON(w, http.StatusOK, blameReply{Blame: gitview.Blame{State: "untracked"}, Version: version})
		return
	case st.State == "notrepo":
		writeJSON(w, http.StatusOK, blameReply{Blame: gitview.Blame{State: "notrepo"}})
		return
	case st.State != "ok" && st.State != "toomany":
		log.Printf("git blame %q: status: %q", t.Top, st.Reason)
		writeJSON(w, http.StatusOK, blameUnavailable(gitReason(st.Reason)))
		return
	}
	k := gitview.BlameKey{Head: st.Head, Version: version, IgnoreRevs: ignoreRevsFile(t)}
	still := func() bool {
		v, ok, err := p.blameVersion(abs)
		return err == nil && ok && v == version
	}
	b := s.Git.Blame(r.Context(), t, rel, k, still)
	reply := blameReply{Blame: b, Version: version}
	switch b.State {
	case "ok", "untracked":
		reply.Reason = ""
		if b.State == "ok" {
			reply.Remote = p.readOrigin(t)
		}
	case "slow":
		reply = blameReply{Blame: gitview.Blame{State: "slow", Reason: msgBlameSlow}}
	case "unavailable":
		log.Printf("git blame %q: %q", t.Top, b.Reason)
		reply = blameUnavailable(gitReason(b.Reason))
	default: // toobig, changed, notrepo
		reply = blameReply{Blame: gitview.Blame{State: b.State}}
	}
	writeJSON(w, http.StatusOK, reply)
}

func blameUnavailable(reason string) blameReply {
	return blameReply{Blame: gitview.Blame{State: "unavailable", Reason: reason}}
}

// blameVersion reads the file at abs through the fs policy, as the editor's read
// does, and returns the sha256 the read would report. text is false for a file
// the editor does not open as text: over maxTextBytes, or binary by the read's
// own test.
func (p fsPolicy) blameVersion(abs string) (version string, text bool, err error) {
	req, err := p.resolve(abs)
	if err != nil {
		return "", false, err
	}
	f, err := openFsTarget(req, 0)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	if !fi.Mode().IsRegular() {
		return "", false, errGitNotFile
	}
	if fi.Size() > maxTextBytes {
		return "", false, nil
	}
	b, err := io.ReadAll(io.LimitReader(f, maxTextBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(b) > maxTextBytes || isBinary(b[:min(len(b), 8000)]) {
		return "", false, nil
	}
	return hashText(b), true, nil
}

// ignoreRevsFile reports whether t's top holds gitview.IgnoreRevsName as a
// regular file, by Lstat on the host: a link is not followed and not passed.
func ignoreRevsFile(t gitview.Target) bool {
	fi, err := os.Lstat(filepath.Join(t.Top, gitview.IgnoreRevsName))
	return err == nil && fi.Mode().IsRegular()
}
