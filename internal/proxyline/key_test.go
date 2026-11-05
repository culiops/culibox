package proxyline

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func keyOf(b byte) Key {
	var k Key
	for i := range k {
		k[i] = b + byte(i)
	}
	return k
}

func TestKeyFromBytesNeeds32NonZeroBytes(t *testing.T) {
	for _, n := range []int{31, 33} {
		if _, err := KeyFromBytes(bytes.Repeat([]byte{7}, n)); err == nil {
			t.Errorf("%d bytes: want an error", n)
		}
	}
	if _, err := KeyFromBytes(make([]byte, 32)); err == nil {
		t.Error("32 zero bytes: want an error")
	}
	one := make([]byte, 32)
	one[31] = 1
	k, err := KeyFromBytes(one)
	if err != nil {
		t.Fatalf("32 bytes with a non-zero byte: %v", err)
	}
	if k.IsZero() {
		t.Error("key from a non-zero byte slice reports IsZero")
	}
	if !(Key{}).IsZero() {
		t.Error("the zero Key must report IsZero")
	}
}

func TestKeyPreambleIsTheWireFormat(t *testing.T) {
	k := keyOf(1)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(k.Hex()) {
		t.Fatalf("Hex is not 64 lowercase hex characters: %q", k.Hex())
	}
	if want := "AGENTBOX-RELAY " + k.Hex() + "\r\n"; string(k.Preamble()) != want {
		t.Fatalf("Preamble = %q, want %q", k.Preamble(), want)
	}
	if len(k.Preamble()) != PreambleLen {
		t.Fatalf("len(Preamble) = %d, want %d", len(k.Preamble()), PreambleLen)
	}
}

func TestKeyIDIsStableAndNotTheKey(t *testing.T) {
	k, other := keyOf(1), keyOf(2)
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(k.ID()) {
		t.Fatalf("ID is not 16 hex characters: %q", k.ID())
	}
	if k.ID() != k.ID() {
		t.Error("ID is not stable")
	}
	if k.ID() == other.ID() {
		t.Error("two keys share an ID")
	}
	if strings.Contains(k.Hex(), k.ID()) {
		t.Error("ID is a substring of the key's hex")
	}
}

// keyRepresentations returns every way fmt can spell the key's bytes: hex, the
// decimal, octal and binary numbers of %d, %o and %b, and the raw characters of
// %c. A formatted value that holds any of them leaked the key.
func keyRepresentations(k Key) map[string]string {
	join := func(verb string) string {
		parts := make([]string, len(k))
		for i, b := range k {
			parts[i] = fmt.Sprintf(verb, b)
		}
		return strings.Join(parts, " ")
	}
	return map[string]string{
		"hex":     k.Hex(),
		"decimal": join("%d"),
		"octal":   join("%o"),
		"binary":  join("%b"),
		"chars":   join("%c"),
	}
}

// Every verb prints the redacted ID, wherever fmt can reach the Key through an
// exported path. A failure names the verb and the shape, never the output: the
// output would be the key.
func TestKeyNeverFormatsItsBytes(t *testing.T) {
	k := keyOf(9)
	type exported struct{ K Key }
	wrapped := exported{k}
	reps := keyRepresentations(k)
	args := map[string]any{
		"key":            k,
		"key pointer":    &k,
		"struct":         wrapped,
		"struct pointer": &wrapped,
		"slice":          []Key{k},
		"map":            map[string]Key{"k": k},
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%o", "%b", "%c", "%U", "%e", "%08d", "% x"} {
		for name, arg := range args {
			got := fmt.Sprintf(verb, arg)
			for rep, leaked := range reps {
				if strings.Contains(got, leaked) {
					t.Errorf("%s on a %s printed the key's %s form", verb, name, rep)
				}
			}
			if !strings.Contains(got, k.ID()) {
				t.Errorf("%s on a %s did not print the key's ID", verb, name)
			}
		}
	}
}

// The redaction rests on fmt calling Key.String, which fmt only does for a Key
// it can reach through an exported path. Pin both halves, so the doc on Key
// stays true: an exported field, slice element and map value print the ID, and
// an unexported field does not (the limitation the doc names).
func TestKeyRedactionStopsAtUnexportedFields(t *testing.T) {
	k := keyOf(9)
	type exported struct{ K Key }
	for name, arg := range map[string]any{
		"exported field": exported{k},
		"slice element":  []Key{k},
		"map value":      map[string]Key{"k": k},
	} {
		for _, verb := range []string{"%v", "%+v", "%x"} {
			if got := fmt.Sprintf(verb, arg); strings.Contains(got, k.Hex()) || strings.Contains(got, fmt.Sprint([]byte(k[:]))) {
				t.Errorf("%s: %s printed the key's bytes", name, verb)
			}
		}
	}

	type unexported struct{ k Key }
	u := unexported{k}
	for verb, leaked := range map[string]string{"%v": fmt.Sprint([]byte(k[:])), "%x": k.Hex()} {
		if got := fmt.Sprintf(verb, u); !strings.Contains(got, leaked) {
			t.Errorf("an unexported Key field is now redacted by %s; update the doc on Key (limitation gone): %s", verb, got)
		}
	}
}

type refusalClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *refusalClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *refusalClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func TestRefusalLogRateLimits(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	clk := &refusalClock{t: t0}
	var mu sync.Mutex
	var lines []string
	r := NewRefusalLog("broker", time.Minute, clk.now, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	got := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}

	for i := 0; i < 3; i++ {
		r.Refused()
	}
	if l := got(); len(l) != 1 {
		t.Fatalf("3 refusals at t0 gave %d lines: %q", len(l), l)
	} else {
		want := "broker: refused 1 connection(s) without CuliBox's relay key since 10:00:00 (a process on this Mac that is not the relay, or a relay started with an old key)"
		if l[0] != want {
			t.Fatalf("line = %q\nwant   %q", l[0], want)
		}
	}

	clk.set(t0.Add(59 * time.Second))
	r.Refused()
	if l := got(); len(l) != 1 {
		t.Fatalf("a refusal at t0+59s logged: %q", l)
	}

	clk.set(t0.Add(61 * time.Second))
	r.Refused()
	l := got()
	if len(l) != 2 {
		t.Fatalf("a refusal at t0+61s gave %d lines: %q", len(l), l)
	}
	if !strings.Contains(l[1], "refused 4 connection(s)") {
		t.Fatalf("second line = %q, want refused 4", l[1])
	}

	// 100 concurrent refusals from t1: every one is either in a logged line
	// or still pending.
	t1 := t0.Add(10 * time.Minute)
	clk.set(t1)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Refused()
		}()
	}
	wg.Wait()
	sum := 0
	for _, line := range got()[2:] {
		var n int
		if _, err := fmt.Sscanf(strings.TrimPrefix(line, "broker: "), "refused %d connection(s)", &n); err != nil {
			t.Fatalf("cannot read the count from %q: %v", line, err)
		}
		sum += n
	}
	r.mu.Lock()
	pending := r.count
	r.mu.Unlock()
	if sum+pending != 100 {
		t.Fatalf("logged %d + pending %d != 100", sum, pending)
	}
}
