// Package handback inspects what a boxed AI-agent session hands back to the
// host (a clone box's returned branch, an in-place box's workspace) and flags
// executables, disguised files, archives, set-id bits and outward symlinks for
// the user to review. It is a review signal, not a malware scan: it never
// runs, decompresses or fully parses a file, and it reads at most HeadBytes of
// each one.
package handback

// Class says why a file was flagged.
type Class string

const (
	// ClassMismatch: the content is runnable or an archive but the extension
	// claims a document, image, media or text type. The strongest signal.
	ClassMismatch Class = "extension-mismatch"
	// ClassExecutable: ELF, Mach-O (thin or fat) or PE, found by content.
	ClassExecutable Class = "executable"
	// ClassScript: a "#!" script that also has an exec bit.
	ClassScript Class = "exec-script"
	// ClassArchive: zip, gzip, bzip2, xz, zstd, 7z, rar or tar.
	ClassArchive Class = "archive"
	// ClassSetID: the setuid or setgid bit is set.
	ClassSetID Class = "setuid-setgid"
	// ClassSymlinkOut: a symlink whose target leaves the workspace.
	ClassSymlinkOut Class = "symlink-outside"
)

// Flag is one flagged file. Every string is box-derived and already passed
// through SanitizePath by whoever built it.
type Flag struct {
	Path     string  `json:"path"`
	Classes  []Class `json:"classes"`
	Detail   string  `json:"detail"`
	SHA256   string  `json:"sha256,omitempty"`
	HashNote string  `json:"hash_note,omitempty"`
	Target   string  `json:"target,omitempty"`
}

// Report is the result of one inspection. Truncated is a human sentence naming
// whatever kept the scan from seeing everything; it is empty when the scan saw
// everything. Incomplete is narrower: it is set only when the walk stopped
// before it had visited every entry (time budget, entry cap, cancellation, the
// candidate cap on head reads), so files in the window may be unseen and the
// next scan must cover the same window again. A reason that comes back on every
// scan of the same tree (an unreadable directory, the depth cap, the flag cap)
// is in Truncated only: it must not pin the window forever.
type Report struct {
	Flags      []Flag `json:"flags"`
	Inspected  int    `json:"inspected"`
	Truncated  string `json:"truncated,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
}
