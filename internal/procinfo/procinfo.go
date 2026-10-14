// Package procinfo reads facts about a process — its parent, start time, argv
// and environment — without assuming Linux's /proc. Linux reads /proc; darwin
// asks the kernel through sysctl. Callers treat any error as "not proven":
// this feeds kill and adopt decisions, so uncertainty must never read as yes.
package procinfo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// splitNul splits a NUL-separated buffer into its non-empty fields.
func splitNul(b []byte) []string {
	var out []string
	for _, p := range strings.Split(string(b), "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// statField returns field n (1-based, as in proc(5)) of a /proc/<pid>/stat
// line. comm (field 2) may contain spaces and ')', so counting starts after the
// LAST ')': the remainder begins at field 3.
func statField(stat string, n int) (string, bool) {
	i := strings.LastIndex(stat, ")")
	if i < 0 || n < 3 {
		return "", false
	}
	f := strings.Fields(stat[i+1:])
	if len(f) < n-2 {
		return "", false
	}
	return f[n-3], true
}

// parseProcArgs2 parses a darwin KERN_PROCARGS2 buffer: a native-endian int32
// argc, the exec path, NUL padding, argc NUL-terminated argv strings, then the
// environment up to an empty string (the kernel's "apple" strings follow and
// are not environment). Both darwin architectures are little-endian. An empty
// argv[0] would be eaten by the padding skip; no real process has one.
func parseProcArgs2(b []byte) (args, env []string, err error) {
	if len(b) < 4 {
		return nil, nil, errors.New("procinfo: procargs2 too short")
	}
	argc := int(int32(binary.LittleEndian.Uint32(b[:4])))
	if argc < 0 || argc > len(b) {
		return nil, nil, fmt.Errorf("procinfo: procargs2 bad argc %d", argc)
	}
	rest := b[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, nil, errors.New("procinfo: procargs2 unterminated exec path")
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	next := func() (string, bool) {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			return "", false
		}
		s := string(rest[:j])
		rest = rest[j+1:]
		return s, true
	}
	for k := 0; k < argc; k++ {
		s, ok := next()
		if !ok {
			return nil, nil, errors.New("procinfo: procargs2 truncated argv")
		}
		args = append(args, s)
	}
	for {
		s, ok := next()
		if !ok || s == "" {
			break
		}
		env = append(env, s)
	}
	return args, env, nil
}
