// Package service runs `culibox serve` under the user's own supervisor:
// a systemd --user unit on Linux, a LaunchAgent on macOS. It writes one file,
// marked as ours, and only ever replaces or removes a file that carries that
// marker. The file holds no secret: the program, its arguments, a few
// non-secret environment variables and the log path.
package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// Marker is the first line of every file install writes (a comment in both
// formats). A file without it is not ours: install never overwrites it and
// uninstall never removes it.
const Marker = "Written by culibox service install; culibox service uninstall removes it. Do not edit."

const (
	// SystemdUnit is not culibox.service: a host may already run its own unit
	// under that name (the DEV instance does).
	SystemdUnit  = "culibox-serve.service"
	LaunchdLabel = "dev.culiops.culibox"
	// ExitRefused is serve's exit code when, run as the service, it refuses its
	// config (a non-loopback bind, password mode): systemd does not restart it.
	ExitRefused = 78
)

type EnvVar struct{ Key, Value string }

// Spec is what the service runs.
type Spec struct {
	Exe     string   // absolute
	Args    []string // e.g. serve, or serve -config /abs/config.json
	Env     []EnvVar // in output order; never a secret
	LogPath string   // absolute; stdout and stderr both
}

// check refuses what neither format can carry without guesswork: control
// characters, a double quote or a backslash, anywhere, and relative paths.
func (s Spec) check() error {
	if !filepath.IsAbs(s.Exe) {
		return fmt.Errorf("service: program path %q is not absolute", s.Exe)
	}
	if !filepath.IsAbs(s.LogPath) {
		return fmt.Errorf("service: log path %q is not absolute", s.LogPath)
	}
	vals := append([]string{s.Exe, s.LogPath}, s.Args...)
	for _, e := range s.Env {
		if e.Key == "" || strings.Contains(e.Key, "=") {
			return fmt.Errorf("service: bad environment variable name %q", e.Key)
		}
		vals = append(vals, e.Key, e.Value)
	}
	for _, v := range vals {
		if strings.ContainsAny(v, `"\`) || strings.ContainsFunc(v, unicode.IsControl) {
			return fmt.Errorf("service: %q holds a quote, a backslash or a control character, which the service file cannot carry safely", v)
		}
	}
	return nil
}

// RenderSystemd is the systemd --user unit. KillMode=process: stopping or
// restarting the service ends only serve, never the abduco sessions it
// started (they are in the same cgroup), so they survive and reattach.
func RenderSystemd(s Spec) ([]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	pct := func(v string) string { return strings.ReplaceAll(v, "%", "%%") }
	word := func(v string) string { return `"` + strings.ReplaceAll(pct(v), "$", "$$") + `"` }
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n[Unit]\nDescription=CuliBox control plane (culibox serve)\n\n[Service]\nType=simple\n", Marker)
	exec := []string{word(s.Exe)}
	for _, a := range s.Args {
		exec = append(exec, word(a))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(exec, " "))
	for _, e := range s.Env {
		fmt.Fprintf(&b, "Environment=\"%s=%s\"\n", e.Key, pct(e.Value))
	}
	fmt.Fprintf(&b, "KillMode=process\nRestart=on-failure\nRestartSec=5\nRestartPreventExitStatus=%d\n", ExitRefused)
	fmt.Fprintf(&b, "StandardOutput=append:%s\nStandardError=append:%s\n", pct(s.LogPath), pct(s.LogPath))
	b.WriteString("\n[Install]\nWantedBy=default.target\n")
	return []byte(b.String()), nil
}

// RenderLaunchd is the LaunchAgent plist. AbandonProcessGroup: launchd would
// otherwise kill the job's process group when it stops, sessions included.
// KeepAlive restarts serve after a crash, not after a clean stop.
func RenderLaunchd(s Spec) ([]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	esc := func(v string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(v))
		return b.String()
	}
	str := func(v string) string { return "<string>" + esc(v) + "</string>" }
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- " + Marker + " -->\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t" + str(LaunchdLabel) + "\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range append([]string{s.Exe}, s.Args...) {
		b.WriteString("\t\t" + str(a) + "\n")
	}
	b.WriteString("\t</array>\n\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	for _, e := range s.Env {
		b.WriteString("\t\t<key>" + esc(e.Key) + "</key>\n\t\t" + str(e.Value) + "\n")
	}
	b.WriteString("\t</dict>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	b.WriteString("\t<key>AbandonProcessGroup</key>\n\t<true/>\n")
	b.WriteString("\t<key>ProcessType</key>\n\t" + str("Standard") + "\n")
	b.WriteString("\t<key>StandardOutPath</key>\n\t" + str(s.LogPath) + "\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t" + str(s.LogPath) + "\n")
	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String()), nil
}

var errNoExe = errors.New("service: no program in the file")

// ParseExe is the program a rendered file runs: the first ExecStart word, or
// the first ProgramArguments string. ok is false when the file has neither.
func ParseExe(goos string, data []byte) (string, bool) {
	var (
		exe string
		err error
	)
	if goos == "darwin" {
		exe, err = plistExe(string(data))
	} else {
		exe, err = unitExe(string(data))
	}
	return exe, err == nil && exe != ""
}

func unitExe(s string) (string, error) {
	for _, line := range strings.Split(s, "\n") {
		rest, ok := strings.CutPrefix(line, `ExecStart="`)
		if !ok {
			continue
		}
		w, _, ok := strings.Cut(rest, `"`)
		if !ok {
			return "", errNoExe
		}
		return strings.ReplaceAll(strings.ReplaceAll(w, "$$", "$"), "%%", "%"), nil
	}
	return "", errNoExe
}

func plistExe(s string) (string, error) {
	_, rest, ok := strings.Cut(s, "<key>ProgramArguments</key>")
	if !ok {
		return "", errNoExe
	}
	i := strings.Index(rest, "<string>")
	j := strings.Index(rest, "</string>")
	if i < 0 || j < i {
		return "", errNoExe
	}
	var v struct {
		Text string `xml:",chardata"`
	}
	if err := xml.Unmarshal([]byte(rest[i:j+len("</string>")]), &v); err != nil {
		return "", err
	}
	return v.Text, nil
}
