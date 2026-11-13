package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/culiops/culibox/internal/config"
)

const (
	homebrewInstall = `/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"`
	dockerEngineDoc = "https://docs.docker.com/engine/install/"
	claudeSetupDoc  = "https://docs.anthropic.com/en/docs/claude-code/setup"
)

// initDep is a program init needs on PATH.
type initDep struct {
	name string   // what the user knows it as
	bin  string   // what is looked up (a configured binary, or the default name)
	brew []string // arguments after `brew install`
	pkg  string   // distro package name; "" = no distro package, see link
	link string   // linux: where to install it from when there is no pkg
}

// label is the name, with the binary when config points somewhere else.
func (p initDep) label() string {
	if p.bin != p.name {
		return fmt.Sprintf("%s (%s)", p.name, p.bin)
	}
	return p.name
}

// initClaudeBin is the claude agent's binary: by name or by binary name (as
// doctor's box-tier rows and the config writer pick it), else plain `claude`.
// Step 1 looks for it and step 5 runs `setup-token` with it.
func initClaudeBin(cfg config.Config) string {
	for _, a := range cfg.Agents {
		if a.Bin != "" && (a.Name == "claude" || filepath.Base(a.Bin) == "claude") {
			return a.Bin
		}
	}
	return "claude"
}

// initDepList is what step 1 checks, in order: abduco, docker, colima (macOS
// only: the box VM), then the default agent's binary.
func initDepList(cfg config.Config, goos string) []initDep {
	or := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	agent := initClaudeBin(cfg)
	deps := []initDep{
		{name: "abduco", bin: or(cfg.AbducoBin, "abduco"), brew: []string{"abduco"}, pkg: "abduco"},
		{name: "docker", bin: or(cfg.DockerBin, "docker"), brew: []string{"docker"}, link: dockerEngineDoc},
	}
	if goos == "darwin" {
		deps = append(deps, initDep{name: "colima", bin: or(cfg.ColimaBin, "colima"), brew: []string{"colima"}})
	}
	return append(deps, initDep{name: "claude", bin: agent, brew: []string{"--cask", "claude-code"}, link: claudeSetupDoc})
}

// initHave says whether p is installed. Colima also gets the Homebrew prefixes,
// like boxvm.LookColima: a terminal started without them on PATH still has it.
func initHave(d initDeps, p initDep) bool {
	if _, err := d.lookPath(p.bin); err == nil {
		return true
	}
	if p.name == "colima" && !strings.Contains(p.bin, "/") {
		for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
			if _, err := d.lookPath(filepath.Join(dir, p.bin)); err == nil {
				return true
			}
		}
	}
	return false
}

func brewLine(p initDep) string { return "brew install " + strings.Join(p.brew, " ") }

// initDepsStep is step 1: the programs init needs. macOS offers a `brew install`
// per missing one (only on a yes; Homebrew itself is never installed). Linux
// prints the install lines and never runs them. false = stopped.
func initDepsStep(u *initUI, cfg config.Config, d initDeps) bool {
	u.begin("dependencies")
	darwin := d.goos == "darwin"
	if darwin {
		if major := d.macOSMajor(); major != 0 && major < 13 {
			u.stopped(fmt.Sprintf("the box VM needs macOS 13 or newer (this is macOS %d); update macOS, then run culibox init again", major))
			return false
		}
	}
	all := initDepList(cfg, d.goos)
	var missing []initDep
	for _, p := range all {
		if !initHave(d, p) {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		names := make([]string, len(all))
		for i, p := range all {
			names[i] = p.name
		}
		u.already(strings.Join(names, ", ") + " found")
		return true
	}
	for _, p := range missing {
		u.say("missing: %s", p.label())
	}
	if !darwin {
		u.stopped(linuxInstallLines(missing, d.osRelease()) + "\nthen run culibox init again")
		return false
	}
	return initBrewInstall(u, d, missing)
}

// initBrewInstall asks, per missing program in order, whether to brew install
// it. Anything declined, failed or still not found after the install stops
// init with the brew lines for what is left.
func initBrewInstall(u *initUI, d initDeps, missing []initDep) bool {
	if _, err := d.lookPath("brew"); err != nil {
		var b strings.Builder
		b.WriteString("Homebrew is not installed. Install it from https://brew.sh :\n")
		b.WriteString(homebrewInstall + "\n")
		b.WriteString("then run culibox init again; it will offer to install:")
		for _, p := range missing {
			b.WriteString("\n" + brewLine(p))
		}
		u.stopped(b.String())
		return false
	}
	var still, installed []initDep
	for i, p := range missing {
		if !u.ask(fmt.Sprintf("Install %s with `%s`?", p.name, brewLine(p)), false) {
			still = append(still, p)
			continue
		}
		args := append([]string{"install"}, p.brew...)
		if err := d.run("brew", args...); err != nil {
			u.say("`%s` failed: %v", brewLine(p), err)
			still = append(still, missing[i:]...)
			break
		}
		if !initHave(d, p) {
			u.say("%s is installed but still not found on PATH (looked for %s)", p.name, p.bin)
			still = append(still, p)
			continue
		}
		installed = append(installed, p)
	}
	for _, p := range installed {
		u.say("installed: %s", p.name)
	}
	if len(still) > 0 {
		var b strings.Builder
		b.WriteString("still missing; run:")
		for _, p := range still {
			b.WriteString("\n" + brewLine(p))
		}
		b.WriteString("\nthen run culibox init again")
		u.stopped(b.String())
		return false
	}
	u.done()
	return true
}

// linuxInstallLines is how to install each missing program on this distro. It
// is only ever printed.
func linuxInstallLines(missing []initDep, distro string) string {
	var lines []string
	for _, p := range missing {
		switch {
		case p.pkg == "":
			lines = append(lines, fmt.Sprintf("%s: install it, see %s", p.name, p.link))
		case distro == "debian" || distro == "ubuntu":
			lines = append(lines, "sudo apt install "+p.pkg)
		case distro == "fedora":
			lines = append(lines, "sudo dnf install "+p.pkg)
		case distro == "arch":
			lines = append(lines, "sudo pacman -S "+p.pkg)
		default:
			lines = append(lines, fmt.Sprintf("install %s with your package manager", p.pkg))
		}
	}
	return strings.Join(lines, "\n")
}
