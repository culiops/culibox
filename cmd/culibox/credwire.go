package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credfile"
	"github.com/culiops/culibox/internal/datacompat"
)

// credState is what loading the web-defined credentials file left behind: where
// each entry came from, the entries that were skipped, and whether the file
// itself was unreadable. The serve path hands it to the credentials screen.
type credState struct {
	Src  credfile.Sources
	Errs []credfile.EntryError
	// Bad: the whole file, or a known kind in it, was lost, so any name could
	// have been a web entry. An unknown key or an undecodable entry is not Bad:
	// the rest loaded, and File.CanSave still errs.
	Bad bool
	// LoadErr is the file-level problem when Bad is set.
	LoadErr error
	// File is what credfile.Load returned (only NotUnderstood when the file is
	// unreadable): the credentials screen's manager starts from it.
	File credfile.File
}

// Problems is one line per thing wrong with the file, for doctor.
func (s credState) Problems() []string {
	var out []string
	if s.LoadErr != nil {
		out = append(out, s.LoadErr.Error())
	}
	for _, line := range s.File.NotUnderstood {
		// A whole-file reason is already inside LoadErr.
		if s.LoadErr == nil || !strings.Contains(s.LoadErr.Error(), line) {
			out = append(out, "credentials.json: "+line)
		}
	}
	for _, e := range s.Errs {
		out = append(out, e.Kind+" "+e.Name+": "+e.Err)
	}
	return out
}

// mergeCredentials appends the entries in <dataDir>/credentials.json to cfg's
// seeds. It runs before Config.Validate and before any subcommand reads cfg, so
// the daemon, doctor, and the store/box/clone CLIs all see the same entries: a
// profile that names a web entry validates, and `store rm` knows a web entry
// still uses a value. A bad file or a bad entry is logged and skipped; it never
// stops the process.
func mergeCredentials(cfg config.Config, home string) (config.Config, credState) {
	f, bad, err := credfile.Load(cfg.ResolvedDataDir(home))
	if err != nil {
		log.Printf("credentials: %v", err)
	}
	if cerr := f.CanSave(); cerr != nil {
		log.Printf("credentials: %v; the Credentials screen will not save until the file is fixed and CuliBox restarted; %s", cerr, datacompat.Hint)
	}
	merged, src, errs := credfile.Merge(cfg, f)
	for _, e := range errs {
		log.Printf("credentials: skipped %s %q: %s", e.Kind, e.Name, e.Err)
	}
	return merged, credState{Src: src, Errs: errs, Bad: bad, LoadErr: err, File: f}
}

// count is how many merged entries came from the given source.
func (s credState) count(source string) int {
	n := 0
	for _, v := range s.Src {
		if v == source {
			n++
		}
	}
	return n
}

// summary is the one boot log line about the file.
func (s credState) summary() string {
	line := fmt.Sprintf("credentials: %d from config.json, %d from the web screen, %d skipped",
		s.count(credfile.SourceConfig), s.count(credfile.SourceWeb), len(s.Errs))
	if s.Bad {
		line += "; credentials.json is unreadable and was ignored"
	}
	if s.File.CanSave() != nil {
		line += "; credentials.json has content this CuliBox cannot read"
	}
	return line
}

// validateBoot is Config.Validate for the boot path. A profile that names a web
// entry which was skipped at load (credentials.json unreadable, or the entry
// invalid or shadowed) must not stop the daemon: the screen that fixes it would
// be unreachable. Such a reference is warned about, loudly, and let through; the
// launcher then refuses to spawn the profile ("not defined"). Every other
// validation error, including a reference to a name no skipped entry had, stays
// fatal.
func (s credState) validateBoot(cfg config.Config) (warnings []string, err error) {
	tolerated, err := cfg.ValidateTolerating(func(r config.UndefinedRefError) bool {
		return s.skipped(r.Kind, r.Name)
	})
	if err != nil {
		return nil, err
	}
	for _, t := range tolerated {
		warnings = append(warnings, fmt.Sprintf(
			"credentials: profile %q names %s %q, which was skipped when credentials.json loaded; the profile cannot start until it is fixed on the Credentials screen",
			t.Agent, t.Ref.Kind, t.Ref.Name))
	}
	return warnings, nil
}

// skipped reports whether a web entry of that kind and name was left out at
// load. An unreadable file, or a known kind of the wrong type, lost web entries,
// so any name could be one. An unknown top-level key lost none.
func (s credState) skipped(kind, name string) bool {
	if s.Bad {
		return true
	}
	for _, e := range s.Errs {
		if e.Kind == kind && e.Name == name {
			return true
		}
	}
	return false
}
