package service

import (
	"context"
	"errors"
	"strings"
)

// fakeRunner records every call as "bin arg..." and answers from script: the
// first entry whose key prefixes the call wins. A key may be answered by a
// queue (each call pops one, the last repeats).
type fakeRunner struct {
	calls  []string
	script map[string][]reply
}

type reply struct {
	out string
	err error
}

var errExit = errors.New("exit status 1")

func (f *fakeRunner) Run(_ context.Context, _ []string, bin string, args ...string) (string, error) {
	call := strings.Join(append([]string{bin}, args...), " ")
	f.calls = append(f.calls, call)
	best := ""
	for k := range f.script {
		if strings.HasPrefix(call, k) && len(k) > len(best) {
			best = k
		}
	}
	q := f.script[best]
	if len(q) == 0 {
		return "", nil
	}
	r := q[0]
	if len(q) > 1 {
		f.script[best] = q[1:]
	}
	return r.out, r.err
}

func (f *fakeRunner) on(prefix string, rs ...reply) *fakeRunner {
	if f.script == nil {
		f.script = map[string][]reply{}
	}
	f.script[prefix] = rs
	return f
}
