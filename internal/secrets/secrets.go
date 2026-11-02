// Package secrets resolves named secret values from configured sources at spawn
// time. Values are returned only to the caller (the launcher) for injection into
// a child's environment; this package never logs a value, and error strings
// carry only the secret name / file path — never the value.
package secrets

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/culiops/culibox/internal/config"
)

// Resolver resolves a secret name to its value.
type Resolver interface {
	Resolve(name string) (value string, ok bool, err error)
}

type source interface {
	lookup(name string) (string, bool, error)
}

// envSource resolves from the daemon's own environment.
type envSource struct{}

func (envSource) lookup(name string) (string, bool, error) {
	v, ok := os.LookupEnv(name)
	return v, ok, nil
}

// fileSource resolves from a KEY=VAL file that must be owner-private.
type fileSource struct {
	path string
	once sync.Once
	vals map[string]string
	err  error
}

func (f *fileSource) load() {
	info, err := os.Stat(f.path)
	if err != nil {
		f.err = fmt.Errorf("secrets file %q: %w", f.path, err)
		return
	}
	if info.Mode().Perm()&0o077 != 0 {
		f.err = fmt.Errorf("secrets file %q is group/other-accessible (mode %04o); must be owner-only (0600)", f.path, info.Mode().Perm())
		return
	}
	fh, err := os.Open(f.path)
	if err != nil {
		f.err = fmt.Errorf("secrets file %q: %w", f.path, err)
		return
	}
	defer fh.Close()
	vals := map[string]string{}
	sc := bufio.NewScanner(fh)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, found := strings.CutPrefix(line, "export"); found && len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimLeft(rest, " \t")
		}
		i := strings.IndexByte(line, '=')
		if i < 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		// strip exactly ONE matching layer of surrounding quotes (not any run,
		// so a value that legitimately ends in ' or " is not mangled)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		vals[k] = v
	}
	if err := sc.Err(); err != nil {
		f.err = fmt.Errorf("secrets file %q: %w", f.path, err)
		return
	}
	f.vals = vals
}

func (f *fileSource) lookup(name string) (string, bool, error) {
	f.once.Do(f.load)
	if f.err != nil {
		return "", false, f.err
	}
	v, ok := f.vals[name]
	return v, ok, nil
}

// composite tries sources in order; first ok wins; a source error propagates.
type composite struct{ sources []source }

func (c *composite) Resolve(name string) (string, bool, error) {
	for _, s := range c.sources {
		v, ok, err := s.lookup(name)
		if err != nil {
			return "", false, err
		}
		if ok {
			return v, true, nil
		}
	}
	return "", false, nil
}

// New builds a Resolver from config sources. With no sources, resolution always
// misses (ok=false) — so any declared secret errors loudly at spawn.
func New(sources []config.SecretSource) (Resolver, error) {
	var ss []source
	for _, s := range sources {
		switch s.Type {
		case "env":
			ss = append(ss, envSource{})
		case "file":
			if s.Path == "" {
				return nil, fmt.Errorf("secrets: file source needs a path")
			}
			ss = append(ss, &fileSource{path: s.Path})
		default:
			return nil, fmt.Errorf("secrets: unknown source type %q", s.Type)
		}
	}
	return &composite{sources: ss}, nil
}
