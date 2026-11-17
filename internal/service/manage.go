package service

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// bootoutPolls × bootoutEvery is how long install and uninstall wait for
// launchd to finish tearing a job down: bootout returns before it has, and a
// bootstrap in that window fails.
const (
	bootoutPolls = 20
	bootoutEvery = 250 * time.Millisecond
)

// Reachable checks that the supervisor can be asked at all, before anything is
// written: a systemd user manager on the session bus, or a GUI login domain.
func (p Platform) Reachable(ctx context.Context) error {
	if !p.Supported() {
		return p.unsupported()
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if p.GOOS == "darwin" {
		if _, err := p.Run.Run(ctx, nil, "launchctl", "print", p.domain()); err != nil {
			return fmt.Errorf("launchd has no GUI login session for this user (%v); run it from a terminal in your Mac login, not over ssh", err)
		}
		return nil
	}
	out, err := p.Run.Run(ctx, nil, "systemctl", "--user", "show-environment")
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("systemd is not available on this machine (no systemctl); run culibox serve yourself")
	}
	if err != nil {
		return fmt.Errorf("systemctl --user is not reachable (%s); log in on a console or over ssh as this user, so that a user manager runs and XDG_RUNTIME_DIR is set", strings.TrimSpace(firstLine(out)))
	}
	return nil
}

// Install writes the unit (or replaces ours) and (re)starts the service, so a
// second install picks up a new binary, PATH or config path. Sessions survive
// the restart (KillMode=process, AbandonProcessGroup) and reattach.
func (p Platform) Install(ctx context.Context, s Spec) error {
	if err := p.Reachable(ctx); err != nil {
		return err
	}
	render := RenderSystemd
	if p.GOOS == "darwin" {
		render = RenderLaunchd
	}
	data, err := render(s)
	if err != nil {
		return err
	}
	path := p.UnitPath()
	if err := WriteUnit(path, data); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if p.GOOS == "darwin" {
		if err := p.bootout(ctx); err != nil {
			return err
		}
		for _, args := range [][]string{{"enable", p.target()}, {"bootstrap", p.domain(), path}} {
			if _, err := p.Run.Run(ctx, nil, "launchctl", args...); err != nil {
				return err
			}
		}
		return nil
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", SystemdUnit}, {"restart", SystemdUnit}} {
		if _, err := p.Run.Run(ctx, nil, "systemctl", append([]string{"--user"}, args...)...); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall stops the service and removes the file, only when the file is
// ours. When the service cannot be stopped the file stays: a running service
// nobody can find is worse than a leftover file.
func (p Platform) Uninstall(ctx context.Context) (removed bool, err error) {
	if !p.Supported() {
		return false, p.unsupported()
	}
	path := p.UnitPath()
	st, err := Inspect(path)
	if err != nil {
		return false, err
	}
	switch st {
	case Absent:
		return false, nil
	case Foreign:
		return false, fmt.Errorf("%s: %w", path, ErrForeign)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if p.GOOS == "darwin" {
		if err := p.bootout(ctx); err != nil {
			return false, err
		}
		return RemoveUnit(path)
	}
	if _, err := p.Run.Run(ctx, nil, "systemctl", "--user", "disable", "--now", SystemdUnit); err != nil {
		return false, fmt.Errorf("could not stop the service, so %s is kept: %w", path, err)
	}
	if removed, err = RemoveUnit(path); err != nil {
		return removed, err
	}
	if _, err := p.Run.Run(ctx, nil, "systemctl", "--user", "daemon-reload"); err != nil {
		return removed, fmt.Errorf("removed %s, but systemctl --user daemon-reload failed: %w", path, err)
	}
	return removed, nil
}

// bootout unloads the job when launchd has it and waits until it is gone.
func (p Platform) bootout(ctx context.Context) error {
	loaded, err := p.loaded(ctx)
	if err != nil || !loaded {
		return err
	}
	if _, err := p.Run.Run(ctx, nil, "launchctl", "bootout", p.target()); err != nil {
		return err
	}
	for range bootoutPolls {
		if loaded, err = p.loaded(ctx); err != nil || !loaded {
			return err
		}
		p.Sleep(bootoutEvery)
	}
	return fmt.Errorf("launchd still has %s after bootout; try again in a few seconds", p.target())
}

func (p Platform) loaded(ctx context.Context) (bool, error) {
	out, err := p.Run.Run(ctx, nil, "launchctl", "print", p.target())
	if err == nil {
		return true, nil
	}
	if strings.Contains(out, "Could not find service") {
		return false, nil
	}
	return false, fmt.Errorf("launchctl print %s: %v", p.target(), err)
}
