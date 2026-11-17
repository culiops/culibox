package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/culiops/culibox/internal/service"
)

func TestCheckService(t *testing.T) {
	const exe = "/home/u/.local/bin/culibox"
	ours := service.State{Path: "/u/unit", File: service.Ours, Exe: exe, Running: true, PID: "42", Detail: "active"}
	cases := []struct {
		name   string
		st     service.State
		err    error
		status string
		want   []string
	}{
		{"absent", service.State{Path: "/u/unit"}, nil, Pass, []string{"not installed", "culibox service install"}},
		{"foreign", service.State{Path: "/u/unit", File: service.Foreign}, nil, Warn, []string{"/u/unit", "not CuliBox's"}},
		{"running darwin", ours, nil, Pass, []string{"installed, running (pid 42)"}},
		{"running linger on", with(ours, func(s *service.State) { s.LingerKnown, s.Linger = true, true }), nil, Pass, []string{"running (pid 42)", "linger on"}},
		{"linger off", with(ours, func(s *service.State) { s.LingerKnown = true }), nil, Warn, []string{"stops at logout", "loginctl enable-linger u"}},
		{"stopped", with(ours, func(s *service.State) { s.Running, s.Detail = false, "failed" }), nil, Warn, []string{"not running (failed)", "/data/service.log"}},
		{"other binary", with(ours, func(s *service.State) { s.Exe = "/old/culibox" }), nil, Warn, []string{"/old/culibox", "culibox service install again"}},
		{"unreachable", service.State{Path: "/u/unit", File: service.Ours}, errors.New("systemctl --user is not reachable"), Warn, []string{"could not tell", "not reachable"}},
	}
	for _, c := range cases {
		r := checkService(c.st, c.err, exe, "/data", "u")
		if r.Check != "service" || r.Status != c.status {
			t.Errorf("%s: %+v, want status %s", c.name, r, c.status)
		}
		for _, w := range c.want {
			if !strings.Contains(r.Detail, w) {
				t.Errorf("%s: %q lacks %q", c.name, r.Detail, w)
			}
		}
	}
}

func with(s service.State, f func(*service.State)) service.State { f(&s); return s }
