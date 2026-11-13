package boxvm

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
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

// repairVM is a VM whose rules change when the relay deletes the old broker
// rule, as the real one's would.
type repairVM struct {
	mu        sync.Mutex
	rules     string
	deletes   []int
	reads     int
	deleteErr error // the delete fails and changes nothing
	noEffect  bool  // the delete reports success and changes nothing
	readErr   error // reads fail once a delete has been tried
}

func (v *repairVM) FilterRules(context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.reads++
	if v.readErr != nil && len(v.deletes) > 0 {
		return "", v.readErr
	}
	return v.rules, nil
}

func (v *repairVM) DeleteBrokerReturn(_ context.Context, port int) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.deletes = append(v.deletes, port)
	if v.deleteErr != nil {
		return v.deleteErr
	}
	if !v.noEffect {
		v.rules = strings.Replace(v.rules, strings.Replace(staleBrokerReturn, "9071", strconv.Itoa(port), 1), "", 1)
	}
	return nil
}

// A VM upgraded without a recreate keeps the old broker RETURN, which the
// provision script deletes on every start but a stamped VM never re-runs. The
// live check deletes exactly that rule and reads the table again, and only a
// table that is still wrong is refused (#116 option b).
func TestEnsureDeletesTheOldBrokerReturnLiveAndReReads(t *testing.T) {
	// The rule sits above the gateway DROP, as the pre-#113 script inserted it.
	stale := withHead(dockerUserHead[:strings.Index(dockerUserHead, "-A DOCKER-USER -d 192.168.5.2/32 -j DROP")] +
		staleBrokerReturn + "-A DOCKER-USER -d 192.168.5.2/32 -j DROP\n")
	for name, tc := range map[string]struct {
		rules   string
		deletes []int
	}{
		"one old rule":  {stale, []int{9071}},
		"two old rules": {strings.Replace(stale, staleBrokerReturn, staleBrokerReturn+staleBrokerReturn, 1), []int{9071, 9071}},
	} {
		t.Run(name, func(t *testing.T) {
			vm := &repairVM{rules: tc.rules}
			d := &fakeDocker{logs: bothListening}
			r := testRelay(fakeVM{}, d)
			r.VM = vm
			if err := r.Ensure(context.Background(), gw); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if !slices.Equal(vm.deletes, tc.deletes) {
				t.Fatalf("deletes = %v, want %v", vm.deletes, tc.deletes)
			}
			if vm.reads < 2 {
				t.Fatalf("the table was read %d times: it must be read again after the delete", vm.reads)
			}
			if len(d.find("run")) != 1 {
				t.Fatalf("the relay was not started: %v", d.subcommands())
			}
		})
	}
}

// The repair is only for the rule the provision script deletes. A different
// rule, a head that is wrong in another way, or INPUT rules that are wrong are
// refused as before and nothing is deleted.
func TestEnsureDeletesNothingElse(t *testing.T) {
	for name, rules := range map[string]string{
		"another port":      goodRules + "-A DOCKER-USER -d 192.168.5.2/32 -p tcp -m tcp --dport 9100 -j RETURN\n",
		"ACCEPT target":     goodRules + "-A DOCKER-USER -d 192.168.5.2/32 -p tcp -m tcp --dport 9071 -j ACCEPT\n",
		"another address":   goodRules + "-A DOCKER-USER -d 192.168.5.3/32 -p tcp -m tcp --dport 9071 -j RETURN\n",
		"bare RETURN above": withHead("-A DOCKER-USER -j RETURN\n" + dockerUserHead),
		"option (a) VM":     optionARules + staleBrokerReturn,
	} {
		t.Run(name, func(t *testing.T) {
			vm := &repairVM{rules: rules}
			d := &fakeDocker{logs: bothListening}
			r := testRelay(fakeVM{}, d)
			r.VM = vm
			err := r.Ensure(context.Background(), gw)
			if err == nil || !strings.Contains(err.Error(), "box setup -recreate") {
				t.Fatalf("err = %v, want a refusal that names the recreate", err)
			}
			if len(vm.deletes) != 0 {
				t.Fatalf("deleted %v", vm.deletes)
			}
			if len(d.find("run")) != 0 {
				t.Fatalf("a relay was started: %v", d.subcommands())
			}
		})
	}
}

// A delete that fails, one that changes nothing and a table that cannot be read
// afterwards all end in the refusal that names the recreate, with the relay
// handled as for any DOCKER-USER refusal: a current one is kept, nothing starts.
func TestEnsureRefusesWhenTheLiveDeleteDoesNotHold(t *testing.T) {
	for name, vm := range map[string]*repairVM{
		"delete fails":           {rules: goodRules + staleBrokerReturn, deleteErr: errors.New("ssh: exit status 1")},
		"delete changes nothing": {rules: goodRules + staleBrokerReturn, noEffect: true},
		"re-read fails":          {rules: goodRules + staleBrokerReturn, readErr: errors.New("ssh: connection refused")},
	} {
		t.Run(name, func(t *testing.T) {
			r := testRelay(fakeVM{}, nil)
			d := &fakeDocker{inspect: "true\t" + r.configHash(gw) + "\n", logs: bothListening}
			r.Docker, r.VM = d, vm
			err := r.Ensure(context.Background(), gw)
			if err == nil || !strings.Contains(err.Error(), "box setup -recreate") {
				t.Fatalf("err = %v", err)
			}
			if len(vm.deletes) == 0 {
				t.Fatal("no delete was tried")
			}
			if got := strings.Join(d.subcommands(), ","); got != "inspect" {
				t.Fatalf("calls = %s, want the current relay inspected and kept", got)
			}
		})
	}
}

// The delete runs over colima ssh with fixed arguments, never through a
// start, and refuses a port that is not a port before it runs anything.
func TestDeleteBrokerReturnRunsAFixedIptablesDelete(t *testing.T) {
	r := &fakeRunner{}
	v := setUpVM(t, r)
	r.mu.Lock()
	r.calls = nil
	r.mu.Unlock()
	if err := v.DeleteBrokerReturn(context.Background(), 9071); err != nil {
		t.Fatal(err)
	}
	want := "ssh --profile agent-box -- sudo iptables -w -D DOCKER-USER -d 192.168.5.2 -p tcp --dport 9071 -j RETURN"
	if got := strings.Join(r.calls, "|"); got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if !slices.Equal(r.envs["ssh"], v.env()) {
		t.Fatalf("ssh env = %v, want the pinned env", r.envs["ssh"])
	}
	for _, port := range []int{0, -1, 65536, 1 << 20} {
		r.mu.Lock()
		r.calls = nil
		r.mu.Unlock()
		if err := v.DeleteBrokerReturn(context.Background(), port); err == nil {
			t.Errorf("port %d accepted", port)
		}
		if len(r.calls) != 0 {
			t.Errorf("port %d: ran %v", port, r.calls)
		}
	}
}
