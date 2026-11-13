package web

import "errors"

// The folder list of the macOS box VM, as the Settings page and its API see it.
// This file holds the types only; the service is cmd/agent-box's vmShareSvc.

// VMFolderRow is one shared folder. Pending is "" for a folder the VM has as
// listed, else "added", "removed" or "switched" (its mode differs).
type VMFolderRow struct {
	Path    string   `json:"path"`
	Display string   `json:"display"`
	Mode    string   `json:"mode"`
	Origin  string   `json:"origin"`
	UsedBy  []string `json:"used_by"`
	Running int      `json:"running"`
	Pending string   `json:"pending"`
}

// VMFoldersView is the page. State is "applied", "pending", "whole_home" (the VM
// still shares all of $HOME; the rows are the proposed list) or "not_set_up".
type VMFoldersView struct {
	State        string        `json:"state"`
	Added        int           `json:"added"`
	Removed      int           `json:"removed"`
	Switched     int           `json:"switched"`
	Rows         []VMFolderRow `json:"rows"`
	DataRoot     string        `json:"data_root"`
	RunningBoxed []string      `json:"running_boxed"`
	Apply        *VMApply      `json:"apply"`
	ListError    string        `json:"list_error"`
}

// VMApplyStep names are "stop", "start" and "check"; states are "waiting",
// "running", "done", "failed" and "not_run".
type VMApplyStep struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Sub   string `json:"sub"`
	Ms    int64  `json:"ms"`
}

// VMApply is the last apply job. State is "running", "done" or "failed".
type VMApply struct {
	ID        int           `json:"id"`
	State     string        `json:"state"`
	StartedMs int64         `json:"started_ms"`
	ElapsedMs int64         `json:"elapsed_ms"` // server's now - started, while running
	Steps     []VMApplyStep `json:"steps"`
	Error     string        `json:"error"`  // short, for the page
	Detail    string        `json:"detail"` // fixed text: VMApplyDetail
	// Raw is the error as it came (OS and colima text, data-dir paths). It is
	// never sent to a browser; the log and the local CLI have it.
	Raw string `json:"-"`
}

// VMApplyDetail is the Detail of every failed apply job: the raw error stays in
// the log, not in the browser.
const VMApplyDetail = "The full error is in the CuliBox log."

// VMAddResult says what an add did beyond adding: the folder that already
// covered it, or the listed folders it swallowed.
type VMAddResult struct {
	CoveredBy string   `json:"covered_by"`
	Absorbed  []string `json:"absorbed"`
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
	ErrVMNotSetUp   = errors.New("the box VM is not set up; run `culibox box setup`")
	ErrListCorrupt  = errors.New("the folder list file is damaged; fix or delete vm-shares.json")
	ErrVMBadMode    = errors.New("the mode must be rw or ro")
)
