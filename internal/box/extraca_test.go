package box

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/culiops/culibox/internal/extraca"
)

// testCASet loads one freshly generated CA through the real extraca.Load, so
// the Set carries exactly what production code would hand to the box.
func testCASet(t *testing.T, cn string) extraca.Set {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := extraca.Load([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func newCAManager(t *testing.T, set extraca.Set, caErr error) (*Manager, *fakeClient) {
	t.Helper()
	f := newFake()
	m, err := New(Options{
		DockerBin:  "/usr/bin/docker",
		Image:      "culiops/agent-box:base",
		Instance:   "TEST",
		IdleTTL:    time.Hour,
		StateRoot:  t.TempDir(),
		Client:     f,
		ExtraCA:    set,
		ExtraCAErr: caErr,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, f
}

// What the fake image's trust store holds: a block shaped like a certificate is
// all the extraction checks for.
const (
	fakeBase    = "-----BEGIN CERTIFICATE-----\nQkFTRQ==\n-----END CERTIFICATE-----\n"
	fakeBaseNew = "-----BEGIN CERTIFICATE-----\nTkVX\n-----END CERTIFICATE-----\n"
)

// caDocker serves the four docker calls caBundle makes. imageID can change
// between calls to stand for a rebuilt image.
type caDocker struct {
	mu      sync.Mutex
	imageID string
	base    []byte
	calls   [][]string
	failCp  bool
	// warn is printed before the answer of `image inspect` and `create`, as
	// docker does when it has a warning for stderr (CombinedOutput merges it).
	warn string
}

// testCID is the container id the fake daemon hands out: 64 lowercase hex.
var testCID = strings.Repeat("f", 64)

func (d *caDocker) serve(f *fakeClient) {
	f.runHook = func(args []string) (string, bool, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch args[0] {
		case "image", "create", "cp", "rm":
		default:
			return "", false, nil
		}
		d.calls = append(d.calls, slices.Clone(args))
		switch args[0] {
		case "image":
			return d.warn + d.imageID, true, nil
		case "create":
			return d.warn + testCID, true, nil
		case "cp":
			if d.failCp {
				return "", true, errors.New("docker cp: no such file")
			}
			return "", true, os.WriteFile(args[len(args)-1], d.base, 0o600)
		}
		return "", true, nil
	}
}

func (d *caDocker) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func (d *caDocker) verbs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		out = append(out, strings.Join(c[:min(len(c), 2)], " "))
	}
	return out
}

// With no CA configured the box argv is exactly what it was before extra CAs
// existed: no label, no mount, nothing extra.
func TestRunArgsUnchangedWithoutCA(t *testing.T) {
	m := newTestManager(t)
	uid := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	want := []string{
		"run", "-d", "--init", "--pull=never",
		"--name", "ab-box-abc",
		"--label", LabelWorkspace + "=/w",
		"--label", LabelInstance + "=TEST",
		"--label", LabelClaims + "=1",
		"--cap-drop=ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=512m",
		"--pids-limit", "512",
		"--memory", "4g", "--memory-swap", "4g",
		"--user", uid,
		"--mount", "type=bind,src=/w,dst=/w",
		"--mount", fmt.Sprintf("type=bind,src=%s,dst=%s", m.stateDir("ab-box-abc"), boxHome),
		"--mount", fmt.Sprintf("type=bind,src=%s,dst=%s", OutputDir(m.opts.StateRoot, "ab-box-abc"), PlaywrightOutBox),
		"--mount", "type=bind,src=/h,dst=/b,readonly",
		"culiops/agent-box:base", "sleep", "infinity",
	}
	got := m.runArgs("ab-box-abc", "/w", []Mount{{Host: "/h", Box: "/b"}}, false, false, "", DefaultMemory)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runArgs changed with CAs off:\n got %q\nwant %q", got, want)
	}
}

func TestRunArgsAddsCABundleAndLabel(t *testing.T) {
	set := testCASet(t, "Corp Inspection Root")
	m, _ := newCAManager(t, set, nil)
	got := m.runArgs("ab-box-abc", "/w", []Mount{{Host: "/h", Box: "/b"}}, false, false, "/state/ca/bundle-x.pem", DefaultMemory)
	if !containsPair(got, "--label", extraca.Label+"="+set.Hash()) {
		t.Fatalf("missing CA label: %q", got)
	}
	want := "type=bind,src=/state/ca/bundle-x.pem,dst=" + extraca.ContainerBundle + ",readonly"
	if !containsPair(got, "--mount", want) {
		t.Fatalf("bundle must be a read-only bind over the system path: %q", got)
	}
	// Right after the extra-RO loop: the mount that follows /h->/b.
	i := slices.Index(got, "type=bind,src=/h,dst=/b,readonly")
	if i < 0 || got[i+1] != "--label" || got[i+3] != "--mount" || got[i+4] != want {
		t.Fatalf("label and bundle mount must follow the extra read-only mounts: %q", got)
	}
}

func envHas(env []string, kv string) int {
	n := 0
	for _, e := range env {
		if e == kv {
			n++
		}
	}
	return n
}

func TestWrapDropsCAEnvWhenOff(t *testing.T) {
	m, _ := newTestManagerWithFake(t)
	in := []string{"SSL_CERT_FILE=/evil", "NODE_EXTRA_CA_CERTS=/evil", "AWS_CA_BUNDLE=/evil", "TERM=xterm"}
	argv, env, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"bash"}, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range extraca.EnvNames {
		for _, e := range env {
			if strings.HasPrefix(e, name+"=") {
				t.Fatalf("%s must not survive with CAs off: %v", name, env)
			}
		}
		if containsPair(argv, "-e", name) {
			t.Fatalf("%s must not be forwarded with CAs off: %v", name, argv)
		}
	}
	if !slices.Contains(env, "TERM=xterm") || !containsPair(argv, "-e", "TERM") {
		t.Fatalf("other env must pass through: %v %v", env, argv)
	}
	if in[0] != "SSL_CERT_FILE=/evil" {
		t.Fatal("the caller's slice must not be modified")
	}
}

func TestWrapSetsCAEnvWhenOn(t *testing.T) {
	set := testCASet(t, "Corp Inspection Root")
	m, f := newCAManager(t, set, nil)
	(&caDocker{imageID: "sha256:" + strings.Repeat("a", 64), base: []byte(fakeBase)}).serve(f)
	in := []string{"NODE_EXTRA_CA_CERTS=/evil", "SSL_CERT_FILE=/evil", "TERM=xterm"}
	argv, env, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"bash"}, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range extraca.EnvNames {
		if n := envHas(env, name+"="+extraca.ContainerBundle); n != 1 {
			t.Fatalf("%s=%s appears %d times, want 1: %v", name, extraca.ContainerBundle, n, env)
		}
		if n := countOf(argv, name); n != 1 {
			t.Fatalf("-e %s appears %d times, want 1: %v", name, n, argv)
		}
	}
	for _, e := range env {
		if strings.Contains(e, "/evil") {
			t.Fatalf("an incoming CA value survived: %v", env)
		}
	}
}

func countOf(xs []string, s string) int {
	n := 0
	for _, x := range xs {
		if x == s {
			n++
		}
	}
	return n
}

func TestCABundleExtractsOncePerImage(t *testing.T) {
	set := testCASet(t, "Corp Inspection Root")
	m, f := newCAManager(t, set, nil)
	d := &caDocker{imageID: "sha256:" + strings.Repeat("a", 64) + "", base: []byte(fakeBase)}
	d.serve(f)
	ctx := context.Background()

	p1, err := m.caBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(m.opts.StateRoot, "ca", "bundle-"+strings.Repeat("a", 12)+"-"+set.Hash()+".pem"); p1 != want {
		t.Fatalf("path = %s, want %s", p1, want)
	}
	got, err := os.ReadFile(p1)
	if err != nil {
		t.Fatal(err)
	}
	if want := append([]byte(fakeBase), set.PEM()...); !bytes.Equal(got, want) {
		t.Fatalf("bundle = base followed by the extra PEM; got %q", got)
	}
	if fi, err := os.Stat(p1); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("bundle mode = %v (%v), want 0644", fi.Mode().Perm(), err)
	}
	if want := []string{"image inspect", "create --pull=never", "cp " + testCID + ":" + extraca.ContainerBundle, "rm -f"}; !reflect.DeepEqual(d.verbs(), want) {
		t.Fatalf("docker calls = %q, want %q", d.verbs(), want)
	}
	if es, _ := os.ReadDir(filepath.Dir(p1)); len(es) != 1 {
		t.Fatalf("only the bundle may remain in ca/ (no temp file, no extra.pem): %v", es)
	}

	// Same image: only the image-ID lookup, never an extraction.
	before := d.count()
	p2, err := m.caBundle(ctx)
	if err != nil || p2 != p1 {
		t.Fatalf("second call = %s, %v", p2, err)
	}
	if n := d.count() - before; n != 1 || d.verbs()[before] != "image inspect" {
		t.Fatalf("a cached bundle may cost only the image lookup: %q", d.verbs()[before:])
	}

	// A rebuilt image has another ID, so another bundle from its own trust store.
	d.mu.Lock()
	d.imageID = "sha256:" + strings.Repeat("b", 64)
	d.base = []byte(fakeBaseNew)
	d.mu.Unlock()
	p3, err := m.caBundle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p3 == p1 || !strings.Contains(p3, "bundle-"+strings.Repeat("b", 12)) {
		t.Fatalf("a new image ID must give a new bundle file: %s", p3)
	}
	if b, _ := os.ReadFile(p3); !bytes.HasPrefix(b, []byte(fakeBaseNew)) {
		t.Fatalf("the new bundle must come from the new image: %q", b)
	}
	if b, _ := os.ReadFile(p1); !bytes.HasPrefix(b, []byte(fakeBase)) {
		t.Fatal("the old image's bundle is untouched")
	}
}

func TestCABundleFailureLeavesNothingMountable(t *testing.T) {
	set := testCASet(t, "Corp Inspection Root")
	m, f := newCAManager(t, set, nil)
	d := &caDocker{imageID: "sha256:" + strings.Repeat("c", 64), base: []byte(fakeBase), failCp: true}
	d.serve(f)
	if _, err := m.caBundle(context.Background()); err == nil {
		t.Fatal("a failed extraction must fail caBundle")
	}
	if v := d.verbs(); v[len(v)-1] != "rm -f" {
		t.Fatalf("the created container must still be removed: %q", v)
	}
	if es, _ := os.ReadDir(filepath.Join(m.opts.StateRoot, "ca")); len(es) != 0 {
		t.Fatalf("no bundle or temp file may be left: %v", es)
	}
}

func TestCABundleRejectsOddImageID(t *testing.T) {
	set := testCASet(t, "Corp Inspection Root")
	m, f := newCAManager(t, set, nil)
	(&caDocker{imageID: "sha256:../../etc", base: []byte("x")}).serve(f)
	if _, err := m.caBundle(context.Background()); err == nil {
		t.Fatal("an image ID that is not hex must not become part of a path")
	}
}

func TestEnsureOffNeverCallsCABundle(t *testing.T) {
	m, f := newTestManagerWithFake(t)
	var calls int
	f.runHook = func(args []string) (string, bool, error) {
		if args[0] != "run" {
			calls++
		}
		return "", false, nil
	}
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	last := f.runArgs[len(f.runArgs)-1]
	if calls != 0 || slices.Contains(last, extraca.Label+"="+"") || hasDst(last, extraca.ContainerBundle) {
		t.Fatalf("CAs off must add no docker call, label or mount: calls=%d %q", calls, last)
	}
	for _, a := range last {
		if strings.Contains(a, extraca.Label) {
			t.Fatalf("no CA label when none is configured: %q", last)
		}
	}
}

func TestWrapCreatesBoxWithBundleAndLabel(t *testing.T) {
	set := testCASet(t, "Corp Inspection Root")
	m, f := newCAManager(t, set, nil)
	(&caDocker{imageID: "sha256:" + strings.Repeat("d", 64), base: []byte(fakeBase)}).serve(f)
	if _, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	last := f.runArgs[len(f.runArgs)-1]
	if !hasDst(last, extraca.ContainerBundle) || !containsPair(last, "--label", extraca.Label+"="+set.Hash()) {
		t.Fatalf("new box lacks bundle or label: %q", last)
	}
}

func TestEnsureReusesMatchingCALabel(t *testing.T) {
	set := testCASet(t, "Corp Inspection Root")
	m, f := newCAManager(t, set, nil)
	(&caDocker{imageID: "sha256:" + strings.Repeat("d", 64), base: []byte(fakeBase)}).serve(f)
	dir := t.TempDir()
	ctx := context.Background()
	_, _, name, err := m.Wrap(ctx, dir, "s1", BoxKey{}, []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	if _, _, _, err := m.Wrap(ctx, dir, "s2", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if f.runs() != 1 || f.stops() != 0 {
		t.Fatalf("a matching label (and the bundle bind) must be reused as is: runs=%d stops=%d", f.runs(), f.stops())
	}
}

func TestEnsureRefusesLiveBoxWithOtherCALabel(t *testing.T) {
	for _, tc := range []struct{ name, label string }{{"older build without label", ""}, {"other CA set", "deadbeef0000"}} {
		t.Run(tc.name, func(t *testing.T) {
			set := testCASet(t, "Corp Inspection Root")
			m, f := newCAManager(t, set, nil)
			(&caDocker{imageID: "sha256:" + strings.Repeat("d", 64), base: []byte(fakeBase)}).serve(f)
			dir := t.TempDir()
			ctx := context.Background()
			// A box as an older build (or another CA config) left it, with a live session.
			m.opts.ExtraCA = extraca.Set{}
			_, _, name, err := m.Wrap(ctx, dir, "s1", BoxKey{}, []string{"bash"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			if tc.label != "" {
				f.labels[name][extraca.Label] = tc.label
			}
			f.mu.Unlock()
			m.opts.ExtraCA = set

			_, _, _, err = m.Wrap(ctx, dir, "s2", BoxKey{}, []string{"bash"}, nil, nil)
			if !errors.Is(err, ErrBoxUnavailable) || !strings.Contains(err.Error(), "different CA set") {
				t.Fatalf("want ErrBoxUnavailable naming the different CA set, got %v", err)
			}
			if !strings.Contains(err.Error(), "end them, then spawn again (macOS: or run `agent-box box stop`)") || !strings.Contains(err.Error(), name) {
				t.Fatalf("the refusal must give the remedy and the box: %v", err)
			}
			if f.stops() != 0 || f.runs() != 1 {
				t.Fatalf("a box with a live session is never touched: stops=%d runs=%d", f.stops(), f.runs())
			}
			m.mu.Lock()
			live := m.active[name]
			m.mu.Unlock()
			if live != 1 {
				t.Fatalf("the refused spawn must not count as a session: %d", live)
			}
		})
	}
}

func TestEnsureRecreatesIdleBoxWithOtherCALabel(t *testing.T) {
	for _, tc := range []struct{ name, label string }{{"unlabelled to configured", ""}, {"other CA set", "deadbeef0000"}} {
		t.Run(tc.name, func(t *testing.T) {
			set := testCASet(t, "Corp Inspection Root")
			m, f := newCAManager(t, set, nil)
			(&caDocker{imageID: "sha256:" + strings.Repeat("d", 64), base: []byte(fakeBase)}).serve(f)
			dir := t.TempDir()
			ctx := context.Background()
			m.opts.ExtraCA = extraca.Set{}
			_, _, name, err := m.Wrap(ctx, dir, "s1", BoxKey{}, []string{"bash"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			if tc.label != "" {
				f.labels[name][extraca.Label] = tc.label
			}
			f.mu.Unlock()
			m.ReleaseBox(name)
			m.opts.ExtraCA = set

			_, _, name2, err := m.Wrap(ctx, dir, "s2", BoxKey{}, []string{"bash"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if name2 != name || f.stops() != 1 || f.runs() != 2 {
				t.Fatalf("want the idle box removed and recreated under the same name: stops=%d runs=%d", f.stops(), f.runs())
			}
			last := f.runArgs[len(f.runArgs)-1]
			if !containsPair(last, "--label", extraca.Label+"="+set.Hash()) || !hasDst(last, extraca.ContainerBundle) {
				t.Fatalf("recreated box must carry the CA label and bundle: %q", last)
			}
		})
	}
}

func TestEnsureTurningCAsOffRecreatesIdleBoxWithoutBundle(t *testing.T) {
	set := testCASet(t, "Corp Inspection Root")
	m, f := newCAManager(t, set, nil)
	(&caDocker{imageID: "sha256:" + strings.Repeat("d", 64), base: []byte(fakeBase)}).serve(f)
	dir := t.TempDir()
	ctx := context.Background()
	_, _, name, err := m.Wrap(ctx, dir, "s1", BoxKey{}, []string{"bash"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.ReleaseBox(name)
	m.opts.ExtraCA = extraca.Set{}
	if _, _, _, err := m.Wrap(ctx, dir, "s2", BoxKey{}, []string{"bash"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	last := f.runArgs[len(f.runArgs)-1]
	if f.stops() != 1 || hasDst(last, extraca.ContainerBundle) {
		t.Fatalf("removing the CAs must drop the old trust at the next spawn: stops=%d %q", f.stops(), last)
	}
}

func TestExtraCAErrRefusesSpawn(t *testing.T) {
	cause := errors.New("/etc/ca.pem: no such file")
	m, f := newCAManager(t, extraca.Set{}, cause)
	f.runHook = func(args []string) (string, bool, error) {
		t.Errorf("no docker call may happen: %v", args)
		return "", true, nil
	}
	_, _, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"bash"}, nil, nil)
	if !errors.Is(err, ErrBoxUnavailable) {
		t.Fatalf("want ErrBoxUnavailable, got %v", err)
	}
	if want := fmt.Sprintf("%v: extra_ca_certs: %v", ErrBoxUnavailable, cause); err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if f.runs() != 0 {
		t.Fatalf("no box may start: runs=%d", f.runs())
	}
}

// An image whose trust store came out empty must not become a bundle holding
// only the operator's CA: every public site would then fail verification.
func TestCABundleRefusesABaseWithoutCertificates(t *testing.T) {
	set := testCASet(t, "Corp Inspection Root")
	m, f := newCAManager(t, set, nil)
	(&caDocker{imageID: "sha256:" + strings.Repeat("e", 64), base: []byte("not a bundle\n")}).serve(f)
	if _, err := m.caBundle(context.Background()); err == nil {
		t.Fatal("a base bundle with no certificate must be refused")
	}
	if es, _ := os.ReadDir(filepath.Join(m.opts.StateRoot, "ca")); len(es) != 0 {
		t.Fatalf("nothing may be written: %v", es)
	}
}

// Docker warnings share the stream with the answer (CombinedOutput). The image
// ID and the container id are the last line, and the scratch container is
// created from the inspected ID, so the bundle's name and its content come from
// the same image.
func TestCABundleIgnoresDockerWarnings(t *testing.T) {
	set := testCASet(t, "Corp Inspection Root")
	m, f := newCAManager(t, set, nil)
	d := &caDocker{imageID: "sha256:" + strings.Repeat("a", 64), base: []byte(fakeBase),
		warn: "WARNING: IPv4 forwarding is disabled. Networking will not work.\n\n"}
	d.serve(f)
	if _, err := m.caBundle(context.Background()); err != nil {
		t.Fatalf("a warning line before the answer must not fail the build: %v", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var create, cp, rm []string
	for _, c := range d.calls {
		switch c[0] {
		case "create":
			create = c
		case "cp":
			cp = c
		case "rm":
			rm = c
		}
	}
	if cp[1] != testCID+":"+extraca.ContainerBundle {
		t.Fatalf("cp used %q, want the clean container id", cp[1])
	}
	if !slices.Equal(rm, []string{"rm", "-f", testCID}) {
		t.Fatalf("rm = %q, want the clean container id", rm)
	}
	n := len(create)
	if create[n-2] != "sha256:"+strings.Repeat("a", 64) || create[n-1] != "true" {
		t.Fatalf("create must name the inspected image ID and an explicit harmless command: %q", create)
	}
	if !slices.Contains(create, "--label") || !slices.Contains(create, LabelCAScratch+"=TEST") {
		t.Fatalf("the scratch container carries the scratch label (not the box labels): %q", create)
	}
	for _, c := range create {
		if strings.HasPrefix(c, LabelInstance) || strings.HasPrefix(c, LabelWorkspace) {
			t.Fatalf("the scratch container must not look like a box: %q", create)
		}
	}
}

func TestCABundleRefusesAnOddContainerID(t *testing.T) {
	for name, cid := range map[string]string{"short": "cid-123", "upper": strings.Repeat("F", 64), "trailing warning": testCID + "\nWARNING: x"} {
		t.Run(name, func(t *testing.T) {
			set := testCASet(t, "Corp Inspection Root")
			m, f := newCAManager(t, set, nil)
			d := &caDocker{imageID: "sha256:" + strings.Repeat("a", 64), base: []byte(fakeBase)}
			d.serve(f)
			hook := f.runHook
			f.runHook = func(args []string) (string, bool, error) {
				if args[0] == "create" {
					return cid, true, nil
				}
				return hook(args)
			}
			if _, err := m.caBundle(context.Background()); err == nil {
				t.Fatal("a container id that is not 64 lowercase hex must be refused")
			}
			for _, v := range d.verbs() {
				if strings.HasPrefix(v, "cp") {
					t.Fatalf("nothing may be copied from a made-up id: %q", d.verbs())
				}
			}
		})
	}
}

func TestCABundleRejectsAShortImageID(t *testing.T) {
	m, f := newCAManager(t, testCASet(t, "Corp Inspection Root"), nil)
	(&caDocker{imageID: "sha256:abcdef012345", base: []byte(fakeBase)}).serve(f)
	if _, err := m.caBundle(context.Background()); err == nil {
		t.Fatal("an image ID is sha256: and 64 hex; a prefix must be refused")
	}
}

// A crash between create and rm leaves the scratch container behind. Reconcile
// and Sweep reap it by its own label; it carries no box label, so claimCheck
// never mistakes it for a box.
func TestReconcileAndSweepReapCAScratchContainers(t *testing.T) {
	m, f := newCAManager(t, extraca.Set{}, nil)
	f.scratch = []string{"ab-ca-orphan"}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.wasStopped("ab-ca-orphan") {
		t.Fatalf("Reconcile must reap the scratch container: %v", f.stopped)
	}
	f.stopped = nil
	rep, err := m.Sweep(context.Background(), map[string]int{})
	if err != nil {
		t.Fatal(err)
	}
	if !f.wasStopped("ab-ca-orphan") || len(rep.Removed) != 1 {
		t.Fatalf("Sweep must reap the scratch container: stopped=%v report=%+v", f.stopped, rep)
	}
}

// SSL_CERT_DIR and GIT_SSL_CAPATH are directory-based trust paths: Wrap drops an
// incoming value whether CAs are on or off, and agent-box never sets them.
func TestWrapDropsTrustDirEnvOnAndOff(t *testing.T) {
	dirs := []string{"SSL_CERT_DIR", "GIT_SSL_CAPATH"}
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("cas_on=%v", on), func(t *testing.T) {
			m, f := newTestManagerWithFake(t)
			if on {
				m, f = newCAManager(t, testCASet(t, "Corp Inspection Root"), nil)
				(&caDocker{imageID: "sha256:" + strings.Repeat("d", 64), base: []byte(fakeBase)}).serve(f)
			}
			in := []string{"SSL_CERT_DIR=/evil", "GIT_SSL_CAPATH=/evil", "TERM=xterm"}
			argv, env, _, err := m.Wrap(context.Background(), t.TempDir(), "s1", BoxKey{}, []string{"bash"}, in, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range dirs {
				for _, e := range env {
					if strings.HasPrefix(e, name+"=") {
						t.Fatalf("%s must be dropped and never set: %v", name, env)
					}
				}
				if containsPair(argv, "-e", name) {
					t.Fatalf("%s must not be forwarded: %v", name, argv)
				}
			}
			if !slices.Contains(env, "TERM=xterm") {
				t.Fatalf("other env must pass through: %v", env)
			}
		})
	}
}
