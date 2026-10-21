// Package boxvm owns agent-box's own colima VM on macOS: its config, its
// lifecycle and its containment check. The VM is agent-box's, never the
// user's: it lives under the data root and never becomes the user's docker
// context, so a rule provisioned into it is a rule agent-box can rely on.
package boxvm

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/culiops/agent-box/internal/box"
	"github.com/culiops/agent-box/internal/config"
)

// Profile is the colima profile name. Short on purpose: lima puts sockets under
// <COLIMA_HOME>/_lima/colima-<profile>/, and macOS caps a socket path at 103 bytes.
const Profile = "agent-box"

// EgressSubnet is the egress network's subnet for bound boxes (M3c). It is
// fixed, not chosen by docker: the provision script runs before the network
// exists and must name it.
const EgressSubnet = "172.29.72.0/24"

// LimaHostGateway is lima's host gateway (host.lima.internal), measured in the
// M2 spike. Bound boxes get it as `--add-host host.lima.internal:<it>` and the
// loopback check dials it; the check fails closed if the address is wrong.
const LimaHostGateway = "192.168.5.2"

// sunPathMax is macOS sockaddr_un.sun_path, NUL included.
const sunPathMax = 104

// Config locates the VM. Every path derives from Root.
type Config struct {
	Root      string
	Home      string
	ColimaBin string
	DockerBin string
	CPUs      int
	MemoryGiB int
	// BrokerPort is the Mac-loopback TCP port of the credential broker; the
	// provision script lets boxes through the gateway DROP on exactly this port.
	BrokerPort int
	// EgressPort is the Mac-loopback TCP port of the egress proxy; boxes on the
	// egress network are let through the gateway DROP on exactly this port and
	// the broker's.
	EgressPort int
}

// ConfigFor is the VM config for an instance: paths from the data root and
// home, size from the instance config. The one place that maps config.json onto
// a Config, so doctor and box setup cannot disagree about the VM they mean.
func ConfigFor(cfg config.Config, dataDir, home, colimaBin, dockerBin string) Config {
	cpus, mem := cfg.BoxVMSize()
	return Config{Root: dataDir, Home: home, ColimaBin: colimaBin, DockerBin: dockerBin, CPUs: cpus, MemoryGiB: mem, BrokerPort: cfg.BrokerPortOrDefault(), EgressPort: cfg.EgressPortOrDefault()}
}

func (c Config) ColimaHome() string      { return filepath.Join(c.Root, "vm") }
func (c Config) DockerConfigDir() string { return filepath.Join(c.Root, "docker") }
func (c Config) ProfileDir() string      { return filepath.Join(c.ColimaHome(), Profile) }
func (c Config) YAMLPath() string        { return filepath.Join(c.ProfileDir(), "colima.yaml") }
func (c Config) StampPath() string       { return filepath.Join(c.ColimaHome(), Profile+".stamp") }
func (c Config) DockerSocket() string    { return filepath.Join(c.ProfileDir(), "docker.sock") }

// DockerArgs pin every docker call to this VM and to a docker config dir
// agent-box owns, so the user's contexts are neither read nor written.
func (c Config) DockerArgs() []string {
	return []string{"--host", "unix://" + c.DockerSocket(), "--config", c.DockerConfigDir()}
}

// Mounts are the host dirs the VM shares: the home (where workspaces live) and
// the data root when it sits outside the home (box homes must be visible too).
func (c Config) Mounts() []string {
	home := filepath.Clean(c.Home)
	out := []string{home}
	root := filepath.Clean(c.Root)
	if root != home && !strings.HasPrefix(root, home+string(filepath.Separator)) {
		out = append(out, root)
	}
	return out
}

// provisionScript runs as root on every VM start. lima's user-mode network maps
// the host gateway (host.lima.internal, 192.168.5.2 in the spike) to the Mac's
// 127.0.0.1, so without this a box reaches every loopback service on the Mac.
// The same address is the boxes' nameserver (measured 2026-10-16: dropping it
// whole broke DNS in every box and in the image build), so udp/53 passes.
// DOCKER-USER is the chain docker evaluates first and never rewrites; creating
// it here is harmless if docker has not started yet (docker keeps it).
// provisionEgress comes last so the egress subnet's rules end up on top.
func provisionScript(brokerPort, egressPort int) string {
	return provisionBase + fmt.Sprintf(provisionBroker, brokerPort, brokerPort) +
		fmt.Sprintf(provisionEgress, egressPort, egressPort, brokerPort, brokerPort)
}

const provisionBase = `#!/bin/sh
# agent-box: boxes must not reach the Mac's loopback through the host gateway.
set -eu
# IPv6: the default docker bridge has no IPv6, so the IPv4 host-gateway DROP is the whole rule.
gw=$(getent ahostsv4 host.lima.internal | awk 'NR==1 {print $1}')
[ -n "$gw" ] || gw=192.168.5.2
iptables -w -N DOCKER-USER 2>/dev/null || true
iptables -w -C DOCKER-USER -d "$gw" -j DROP 2>/dev/null || iptables -w -I DOCKER-USER 1 -d "$gw" -j DROP
# DNS over UDP is the one exception: docker hands boxes the gateway as their
# nameserver (the VM's systemd-resolved uplink). udp/53 there is expected to be
# lima's own resolver, which takes the packet before the NAT to the Mac; check
# with a decoy on the Mac's 127.0.0.1:53 after a lima upgrade.
# TCP stays dropped: an older resolver without a TCP listener would let tcp/53
# fall through to the Mac's 127.0.0.1:53. Removed and re-inserted above the
# DROP on every run, so the order always holds.
while iptables -w -D DOCKER-USER -d "$gw" -p udp --dport 53 -j RETURN 2>/dev/null; do :; done
iptables -w -I DOCKER-USER 1 -d "$gw" -p udp --dport 53 -j RETURN
`

// provisionBroker is its own constant, formatted with only the broker port, so
// a stray % in provisionBase can never break it. The credential broker listens
// on the Mac's loopback; boxes reach it through the gateway on exactly one TCP
// port, re-inserted above the DROP on every boot like DNS.
const provisionBroker = `# credential broker (loopback on the Mac): one TCP port through the gateway DROP.
while iptables -w -D DOCKER-USER -d "$gw" -p tcp --dport %d -j RETURN 2>/dev/null; do :; done
iptables -w -I DOCKER-USER 1 -d "$gw" -p tcp --dport %d -j RETURN
`

// provisionEgress is formatted with only the two ports (the subnet is a literal
// from EgressSubnet, so no % can come from it). Boxes bound to cloud credentials
// sit on the egress network and may reach the Mac only through the gateway's
// broker and egress-proxy ports; everything else from the subnet is dropped,
// DNS included, and so is any packet to the VM itself. Each rule is removed and
// re-inserted at position 1 on every boot, the DROP first, so the order holds
// whatever the chain looked like before.
const provisionEgress = `# egress (M3c): boxes on the egress network reach only the broker and the
# egress proxy on the Mac. No udp/53 for this subnet: their DNS is closed.
sub=` + EgressSubnet + `
while iptables -w -D DOCKER-USER -s "$sub" -j DROP 2>/dev/null; do :; done
iptables -w -I DOCKER-USER 1 -s "$sub" -j DROP
while iptables -w -D DOCKER-USER -s "$sub" -d "$gw" -p tcp --dport %d -j RETURN 2>/dev/null; do :; done
iptables -w -I DOCKER-USER 1 -s "$sub" -d "$gw" -p tcp --dport %d -j RETURN
while iptables -w -D DOCKER-USER -s "$sub" -d "$gw" -p tcp --dport %d -j RETURN 2>/dev/null; do :; done
iptables -w -I DOCKER-USER 1 -s "$sub" -d "$gw" -p tcp --dport %d -j RETURN
# The VM itself (its bridge gateway, sshd): nothing from this subnet.
iptables -w -C INPUT -s "$sub" -j DROP 2>/dev/null || iptables -w -I INPUT 1 -s "$sub" -j DROP
`

// yamlSafe refuses what a double-quoted YAML scalar would need escaping for or
// could be read differently: a double quote, a backslash, invalid UTF-8, any
// control character (C0, DEL, C1 including U+0085 NEL) and U+2028/U+2029, which
// YAML 1.1 readers treat as line breaks. Refused, not escaped: the same paths
// also reach docker and colima argv.
func yamlSafe(p string) error {
	bad := !utf8.ValidString(p)
	for _, r := range p {
		if r == '"' || r == '\\' || unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			bad = true
		}
	}
	if bad {
		return fmt.Errorf("path %q contains a character agent-box will not write into the VM config", p)
	}
	return nil
}

// Validate refuses a configuration that cannot be carried safely, before any
// colima or docker call. The data root is written into the VM's YAML and is
// bind-mounted into boxes through docker's CSV --mount parser; the home is
// written into the YAML. A path either parser would misread is refused, not
// escaped, and the error names the path and the remedy.
func (c Config) Validate() error {
	for _, p := range []struct{ what, path string }{{"data root", c.Root}, {"home", c.Home}} {
		if !filepath.IsAbs(p.path) {
			return fmt.Errorf("the box VM needs an absolute %s, got %q", p.what, p.path)
		}
		why := ""
		if err := yamlSafe(p.path); err != nil {
			why = err.Error()
		} else if r := box.MountUnsafe(p.path); r != "" {
			why = fmt.Sprintf("%s %q: %s", p.what, p.path, r)
		}
		if why == "" {
			continue
		}
		if p.what == "home" {
			// AGENT_BOX_HOME moves the data root, never the user's home.
			return fmt.Errorf("the box VM cannot share this home directory (%s). Rename the home directory, or run agents on the host tier instead", why)
		}
		return fmt.Errorf("the box VM cannot use this data root (%s). Choose another data root with AGENT_BOX_HOME", why)
	}
	for _, b := range []struct{ what, path string }{{"colima", c.ColimaBin}, {"docker", c.DockerBin}} {
		if !filepath.IsAbs(b.path) {
			return fmt.Errorf("the box VM needs an absolute %s binary path, got %q", b.what, b.path)
		}
	}
	if c.BrokerPort <= 0 || c.BrokerPort > 65535 {
		return fmt.Errorf("the box VM needs a broker port between 1 and 65535, got %d", c.BrokerPort)
	}
	if c.EgressPort <= 0 || c.EgressPort > 65535 {
		return fmt.Errorf("the box VM needs an egress port between 1 and 65535, got %d", c.EgressPort)
	}
	if c.CPUs <= 0 || c.MemoryGiB <= 0 {
		return fmt.Errorf("the box VM needs positive cpus and memory, got %d and %d GiB", c.CPUs, c.MemoryGiB)
	}
	return nil
}

// YAML is the colima.yaml agent-box owns. Every setting that would let colima
// touch the user's state is pinned off.
func (c Config) YAML() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("# Written by `agent-box box setup`. Edits are overwritten: set box_vm_cpus /\n")
	b.WriteString("# box_vm_memory_gib in agent-box's config.json instead.\n")
	fmt.Fprintf(&b, "cpu: %d\nmemory: %d\n", c.CPUs, c.MemoryGiB)
	b.WriteString("runtime: docker\nvmType: vz\nrosetta: false\nmountType: virtiofs\nmountInotify: true\n")
	b.WriteString("autoActivate: false\nsshConfig: false\nforwardAgent: false\n")
	b.WriteString("mounts:\n")
	for _, m := range c.Mounts() {
		if err := yamlSafe(m); err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "  - location: \"%s\"\n    writable: true\n", m)
	}
	b.WriteString("provision:\n  - mode: system\n    script: |\n")
	for _, line := range strings.Split(strings.TrimRight(provisionScript(c.BrokerPort, c.EgressPort), "\n"), "\n") {
		b.WriteString("      " + line + "\n")
	}
	return []byte(b.String()), nil
}

// Env is the environment colima runs with: pinned to the data root, with the
// user's own docker/lima endpoints removed, and a PATH that finds limactl and
// docker even when agent-box was started without Homebrew on PATH.
func (c Config) Env(base []string) []string {
	drop := []string{"COLIMA_HOME=", "LIMA_HOME=", "DOCKER_CONFIG=", "DOCKER_HOST=", "DOCKER_CONTEXT=", "PATH="}
	out := make([]string, 0, len(base)+3)
next:
	for _, kv := range base {
		for _, d := range drop {
			if strings.HasPrefix(kv, d) {
				continue next
			}
		}
		out = append(out, kv)
	}
	var dirs []string
	seen := map[string]bool{}
	for _, d := range []string{filepath.Dir(c.ColimaBin), filepath.Dir(c.DockerBin),
		"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return append(out, "COLIMA_HOME="+c.ColimaHome(), "DOCKER_CONFIG="+c.DockerConfigDir(), "PATH="+strings.Join(dirs, ":"))
}

// StartArgs starts the VM without letting colima switch the user's docker
// context, edit ~/.ssh/config or rewrite our yaml (all three default to true in
// colima 0.8.1).
func (c Config) StartArgs() []string {
	return []string{"start", "--profile", Profile, "--activate=false", "--ssh-config=false", "--save-config=false"}
}

// CheckSocketPaths refuses a root whose VM sockets would exceed macOS's limit.
func (c Config) CheckSocketPaths() error {
	inst := filepath.Join(c.ColimaHome(), "_lima", "colima-"+Profile)
	for _, p := range []string{c.DockerSocket(), filepath.Join(inst, "serialv.sock"), filepath.Join(inst, "ssh.sock")} {
		if len(p) >= sunPathMax {
			return fmt.Errorf("the box VM socket %q is %d bytes; macOS allows %d. Set AGENT_BOX_HOME to a shorter absolute path",
				p, len(p), sunPathMax-1)
		}
	}
	return nil
}

// LookColima resolves colima like box.LookDocker resolves docker, with the
// Homebrew prefixes as fallbacks for a daemon started without them on PATH.
func LookColima(bin string) (string, error) {
	if bin == "" {
		bin = "colima"
	}
	if strings.Contains(bin, "/") {
		return bin, nil
	}
	if p, err := exec.LookPath(bin); err == nil {
		return p, nil
	}
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		p := filepath.Join(dir, bin)
		if _, err := exec.LookPath(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("colima not found; install it with `brew install colima docker`")
}
