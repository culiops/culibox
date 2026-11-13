package boxvm

import (
	"strings"
	"testing"
)

// dockerUserHead is the three rules provisioning leaves at the top of
// DOCKER-USER, in order, in iptables' own output format.
const dockerUserHead = `-A DOCKER-USER -s 172.29.72.0/24 -j DROP
-A DOCKER-USER -d 192.168.5.2/32 -p udp -m udp --dport 53 -j RETURN
-A DOCKER-USER -d 192.168.5.2/32 -j DROP
`

// withHead is goodRules with its DOCKER-USER rules replaced by chain.
func withHead(chain string) string {
	var out []string
	done := false
	for _, l := range strings.SplitAfter(goodRules, "\n") {
		if strings.HasPrefix(l, "-A DOCKER-USER ") {
			if !done {
				out = append(out, chain)
				done = true
			}
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "")
}

// The head of DOCKER-USER is checked exactly, like INPUT's: a rule that is not
// ours above the gateway DROP can change what the DROP covers, whatever its
// protocol or target (a bare RETURN or ACCEPT, "-p all", a jump to a chain that
// accepts, a negated match), and so can a head that lost a rule or has them in
// another order.
func TestCheckDockerUserRulesChecksTheExactHead(t *testing.T) {
	lines := strings.SplitAfter(dockerUserHead, "\n")
	subDrop, udp, gwDrop := lines[0], lines[1], lines[2]
	cases := map[string]string{
		"bare RETURN on top":                "-A DOCKER-USER -j RETURN\n" + dockerUserHead,
		"bare ACCEPT on top":                "-A DOCKER-USER -j ACCEPT\n" + dockerUserHead,
		"bare RETURN above the DROP":        subDrop + udp + "-A DOCKER-USER -j RETURN\n" + gwDrop,
		"-p all ACCEPT":                     subDrop + udp + "-A DOCKER-USER -p all -j ACCEPT\n" + gwDrop,
		"-p 6 RETURN":                       subDrop + udp + "-A DOCKER-USER -p 6 -j RETURN\n" + gwDrop,
		"jump to a user chain":              subDrop + udp + "-A DOCKER-USER -j MYCHAIN\n" + gwDrop,
		"negated gateway DROP":              subDrop + udp + "-A DOCKER-USER ! -d 192.168.5.2/32 -j DROP\n",
		"udp RETURN with an extra match":    subDrop + "-A DOCKER-USER -i eth0 -d 192.168.5.2/32 -p udp -m udp --dport 53 -j RETURN\n" + gwDrop,
		"udp RETURN for another port":       subDrop + "-A DOCKER-USER -d 192.168.5.2/32 -p udp -m udp --dport 5353 -j RETURN\n" + gwDrop,
		"udp RETURN missing":                subDrop + gwDrop,
		"subnet DROP missing":               udp + gwDrop,
		"gateway DROP above the udp RETURN": subDrop + gwDrop + udp,
		"subnet DROP below":                 udp + gwDrop + subDrop,
		"subnet DROP is wider":              "-A DOCKER-USER -s 172.29.0.0/16 -j DROP\n" + udp + gwDrop,
		"chain empty":                       "",
	}
	for name, chain := range cases {
		t.Run(name, func(t *testing.T) {
			err := CheckDockerUserRules(withHead(chain))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), "box setup -recreate") || !strings.Contains(err.Error(), "192.168.5.2") {
				t.Fatalf("the error must name the gateway and the fix: %v", err)
			}
		})
	}
}

// What docker adds after the head (a bare RETURN at the end of the chain) stays fine.
func TestCheckDockerUserRulesAcceptsWhatFollowsTheHead(t *testing.T) {
	for name, tail := range map[string]string{
		"nothing":              "",
		"docker's bare RETURN": "-A DOCKER-USER -j RETURN\n",
	} {
		if err := CheckDockerUserRules(withHead(dockerUserHead + tail)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
