package service

import (
	"strings"
	"testing"
)

func baseSpec() Spec {
	return Spec{
		Exe:     "/home/u/.local/bin/culibox",
		Args:    []string{"serve"},
		Env:     []EnvVar{{"PATH", "/usr/bin:/bin"}, {"LANG", "en_US.UTF-8"}},
		LogPath: "/home/u/.local/share/culibox/service.log",
	}
}

func TestRenderSystemdGolden(t *testing.T) {
	got, err := RenderSystemd(baseSpec())
	if err != nil {
		t.Fatal(err)
	}
	want := "# " + Marker + `
[Unit]
Description=CuliBox control plane (culibox serve)

[Service]
Type=simple
ExecStart="/home/u/.local/bin/culibox" "serve"
Environment="PATH=/usr/bin:/bin"
Environment="LANG=en_US.UTF-8"
KillMode=process
Restart=on-failure
RestartSec=5
RestartPreventExitStatus=78
StandardOutput=append:/home/u/.local/share/culibox/service.log
StandardError=append:/home/u/.local/share/culibox/service.log

[Install]
WantedBy=default.target
`
	if string(got) != want {
		t.Fatalf("unit:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderSystemdEscapes(t *testing.T) {
	s := baseSpec()
	s.Exe = "/opt/my apps/50%/culibox"
	s.Args = []string{"serve", "$x"}
	s.Env = []EnvVar{{"K", "a%b$c"}}
	s.LogPath = "/data/my root/50%/service.log"
	b, err := RenderSystemd(s)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		`ExecStart="/opt/my apps/50%%/culibox" "serve" "$$x"`,
		`Environment="K=a%%b$c"`,
		"StandardOutput=append:/data/my root/50%%/service.log\n",
		"StandardError=append:/data/my root/50%%/service.log\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderRefusesUnsafe(t *testing.T) {
	bad := []string{"a\nb", `a"b`, `a\b`, "a\x00b", "a\tb"}
	mutate := []func(*Spec, string){
		func(s *Spec, v string) { s.Exe = "/bin/" + v },
		func(s *Spec, v string) { s.Args = []string{"serve", v} },
		func(s *Spec, v string) { s.Env = []EnvVar{{"K", v}} },
		func(s *Spec, v string) { s.Env = []EnvVar{{v, "x"}} },
		func(s *Spec, v string) { s.LogPath = "/tmp/" + v },
	}
	for name, render := range map[string]func(Spec) ([]byte, error){"systemd": RenderSystemd, "launchd": RenderLaunchd} {
		for i, m := range mutate {
			for _, v := range bad {
				s := baseSpec()
				m(&s, v)
				if _, err := render(s); err == nil {
					t.Errorf("%s: field %d with %q rendered", name, i, v)
				}
			}
		}
		s := baseSpec()
		s.Exe = "culibox"
		if _, err := render(s); err == nil {
			t.Errorf("%s: relative exe rendered", name)
		}
		s = baseSpec()
		s.LogPath = "service.log"
		if _, err := render(s); err == nil {
			t.Errorf("%s: relative log path rendered", name)
		}
		s = baseSpec()
		s.Env = []EnvVar{{"A=B", "x"}}
		if _, err := render(s); err == nil {
			t.Errorf("%s: env key with = rendered", name)
		}
	}
}

func TestRenderLaunchdGolden(t *testing.T) {
	s := baseSpec()
	s.Env = append(s.Env, EnvVar{"X", "a&b<c"})
	b, err := RenderLaunchd(s)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.HasPrefix(got, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- "+Marker+" -->\n") {
		t.Fatalf("header:\n%s", got)
	}
	compact := strings.Join(strings.Fields(got), "")
	for _, want := range []string{
		"<key>Label</key><string>dev.culiops.culibox</string>",
		"<key>ProgramArguments</key><array><string>/home/u/.local/bin/culibox</string><string>serve</string></array>",
		"<key>EnvironmentVariables</key><dict><key>PATH</key><string>/usr/bin:/bin</string><key>LANG</key><string>en_US.UTF-8</string><key>X</key><string>a&amp;b&lt;c</string></dict>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>",
		"<key>AbandonProcessGroup</key><true/>",
		"<key>ProcessType</key><string>Standard</string>",
		"<key>StandardOutPath</key><string>/home/u/.local/share/culibox/service.log</string>",
		"<key>StandardErrorPath</key><string>/home/u/.local/share/culibox/service.log</string>",
	} {
		if !strings.Contains(compact, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
}

func TestParseExeRoundTrip(t *testing.T) {
	for _, exe := range []string{"/home/u/.local/bin/culibox", "/opt/my apps/50%/a&b<c$d/culibox"} {
		s := baseSpec()
		s.Exe = exe
		for goos, render := range map[string]func(Spec) ([]byte, error){"linux": RenderSystemd, "darwin": RenderLaunchd} {
			b, err := render(s)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := ParseExe(goos, b)
			if !ok || got != exe {
				t.Errorf("%s: ParseExe = %q, %v; want %q", goos, got, ok, exe)
			}
		}
	}
	for _, goos := range []string{"linux", "darwin"} {
		if got, ok := ParseExe(goos, []byte("garbage\n")); ok {
			t.Errorf("%s: ParseExe(garbage) = %q, true", goos, got)
		}
	}
}
