package doctor

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/culiops/culibox/internal/service"
)

// serviceStatus reads the `culibox service` state, a seam for tests. It only
// reads: a file, systemctl is-active / show, launchctl print; the supervisor
// is asked only when the unit file is ours, so a temp Home asks nothing.
var serviceStatus = func(p service.Platform) (service.State, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.Status(ctx)
}

// checkService is the `service` row: whether CuliBox runs as a systemd --user
// service or a LaunchAgent. Optional, so never a FAIL.
func checkService(st service.State, err error, exe, dataDir, user string) Result {
	const check = "service"
	switch {
	case st.File == service.Absent:
		return Result{Pass, check, "not installed (optional: culibox service install runs CuliBox without a terminal)"}
	case st.File == service.Foreign:
		return Result{Warn, check, st.Path + " is not CuliBox's (no culibox service marker); CuliBox leaves it alone"}
	case err != nil:
		return Result{Warn, check, "installed; could not tell whether it runs: " + err.Error()}
	case !st.Running:
		return Result{Warn, check, "installed, not running (" + st.Detail + "); see " + filepath.Join(dataDir, "service.log")}
	case exe != "" && st.Exe != "" && filepath.Clean(exe) != st.Exe:
		return Result{Warn, check, "installed, running " + st.Exe + ", not this culibox; run culibox service install again"}
	case st.LingerKnown && !st.Linger:
		return Result{Warn, check, "installed, running (pid " + st.PID + "); at logout it stops and its running agent sessions are killed, until `loginctl enable-linger " + user + "`"}
	case st.LingerKnown:
		return Result{Pass, check, "installed, running (pid " + st.PID + "), linger on"}
	}
	return Result{Pass, check, "installed, running (pid " + st.PID + ")"}
}

// serviceRow is checkService for this machine.
func serviceRow(in Input, dataDir string) Result {
	p := service.Real(in.Home, in.XDGConfig)
	st, err := serviceStatus(p)
	exe, _ := os.Executable()
	return checkService(st, err, exe, dataDir, p.User)
}
