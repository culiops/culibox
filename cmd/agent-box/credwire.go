package main

import (
	"fmt"
	"log"

	"github.com/culiops/agent-box/internal/config"
	"github.com/culiops/agent-box/internal/credfile"
)

// credState is what loading the web-defined credentials file left behind: where
// each entry came from, the entries that were skipped, and whether the file
// itself was unreadable. The serve path hands it to the credentials screen.
type credState struct {
	Src  credfile.Sources
	Errs []credfile.EntryError
	Bad  bool
	// LoadErr is the file-level problem when Bad is set.
	LoadErr error
}

// Problems is one line per thing wrong with the file, for doctor.
func (s credState) Problems() []string {
	var out []string
	if s.LoadErr != nil {
		out = append(out, s.LoadErr.Error())
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
	merged, src, errs := credfile.Merge(cfg, f)
	for _, e := range errs {
		log.Printf("credentials: skipped %s %q: %s", e.Kind, e.Name, e.Err)
	}
	return merged, credState{Src: src, Errs: errs, Bad: bad, LoadErr: err}
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
	return line
}
