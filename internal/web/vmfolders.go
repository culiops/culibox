package web

import "errors"

// The folder list of the macOS box VM, as the Settings page and its API see it.
// This file holds the types only; the service is cmd/agent-box's vmShareSvc.

// VMFolderRow is one shared folder. Pending is "" for a folder the VM has as
// listed, else "added", "removed" or "switched" (its mode differs).
type VMFolderRow struct {
	Path, Display, Mode, Origin string
	UsedBy                      []string
	Running                     int
	Pending                     string
}

// VMFoldersView is the page. State is "applied", "pending", "whole_home" (the VM
// still shares all of $HOME; the rows are the proposed list) or "not_set_up".
type VMFoldersView struct {
	State                    string
	Added, Removed, Switched int
	Rows                     []VMFolderRow
	DataRoot                 string
	RunningBoxed             []string
	Apply                    *VMApply
	ListError                string
}

// VMApplyStep names are "stop", "start" and "check"; states are "waiting",
// "running", "done", "failed" and "not_run".
type VMApplyStep struct {
	Name, State, Sub string
	Ms               int64
}

// VMApply is the last apply job. State is "running", "done" or "failed".
type VMApply struct {
	ID        int
	State     string
	StartedMs int64
	Steps     []VMApplyStep
	Error     string // short, for the page
	Detail    string // the raw error
}

// VMAddResult says what an add did beyond adding: the folder that already
// covered it, or the listed folders it swallowed.
type VMAddResult struct {
	CoveredBy string
	Absorbed  []string
}

// VMFolders is the folder service the handlers call.
type VMFolders interface {
	View() (VMFoldersView, error)
	Add(typed, mode string, dryRun bool) (VMFoldersView, VMAddResult, error)
	SetMode(path, mode string) (VMFoldersView, error)
	Remove(path string) (VMFoldersView, error)
	Apply() (VMApply, error)
	ApplyStatus() (VMApply, bool)
}

var (
	ErrApplyRunning = errors.New("the VM is already restarting to apply its folders")
	ErrVMNotSetUp   = errors.New("the box VM is not set up; run `agent-box box setup`")
	ErrListCorrupt  = errors.New("the folder list file is damaged; fix or delete vm-shares.json")
)
