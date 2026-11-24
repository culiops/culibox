package boximagecheck

import (
	"strings"
	"testing"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/boxregistry"
)

func sha(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func ref(c string) string { return boximage.PublishedRepo + "@" + sha(c) }

const defaultImage = "culiops/culibox:base"

// platform builds a platform whose layer i has diff id sha(<i>) and the given size.
func platform(sizes ...int64) boxregistry.Platform {
	var p boxregistry.Platform
	for i, s := range sizes {
		c := string(rune('1' + i))
		p.Layers = append(p.Layers, boxregistry.Layer{Digest: sha("a"), Size: s})
		p.DiffIDs = append(p.DiffIDs, sha(c))
	}
	return p
}

func registryCache(want string, p boxregistry.Platform) *Cache {
	return &Cache{
		Version: 1, CheckedAt: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC), Hash: boximage.Hash(),
		Registry: &boxregistry.Index{Ref: want, Platforms: map[string]boxregistry.Platform{"arm64": p}},
	}
}

// info is a present image of an older Dockerfile unless hash says otherwise.
func info(hash string, digests []string, diffIDs ...string) *boximage.ImageInfo {
	return &boximage.ImageInfo{Present: true, ID: sha("e"), RepoDigests: digests, Arch: "arm64", Hash: hash, DiffIDs: diffIDs}
}

func TestEvaluate(t *testing.T) {
	const old = "000000000000"
	pub := ref("a")
	three := platform(100e6, 150e6, 62e6) // diff ids sha(1), sha(2), sha(3)
	cfgLine := func(r string) string { return `"box_image": "` + r + `"` }
	failed := &Cache{Version: 1, Hash: boximage.Hash(), RegistryErr: "registry unreachable"}

	tests := []struct {
		name                     string
		in                       Inputs
		verdict                  Verdict
		want                     string
		src                      Source
		configLine, setupCmd, mb string
	}{
		{"release default current",
			Inputs{Configured: defaultImage, Published: pub, CheckOn: true, Arch: "arm64", Info: info(old, []string{pub})},
			"current", pub, "baked", "", "", ""},
		{"release default stale",
			Inputs{Configured: defaultImage, Published: pub, CheckOn: true, Arch: "arm64", Info: info(old, []string{ref("9")})},
			"update", pub, "baked", "", "culibox box setup", ""},
		{"local build same hash",
			Inputs{Configured: defaultImage, CheckOn: false, Arch: "arm64", Info: info(boximage.Hash(), nil)},
			"current-local", "", "", "", "", ""},
		{"explicit pin stale, registry cached",
			Inputs{Configured: ref("9"), Explicit: true, Published: pub, CheckOn: true, Arch: "arm64",
				Info:  info(old, []string{ref("9")}, sha("1")),
				Cache: registryCache(ref("b"), three)},
			"update", ref("b"), "registry", cfgLine(ref("b")), "culibox box setup", "212"},
		{"explicit pin is the registry digest",
			Inputs{Configured: ref("b"), Explicit: true, Published: pub, CheckOn: true, Arch: "arm64",
				Info: info(old, nil), Cache: registryCache(ref("b"), three)},
			"current", ref("b"), "registry", "", "", ""},
		{"dated pin",
			Inputs{Configured: "culiops/culibox:2026-11-01", Explicit: true, Published: pub, CheckOn: true, Arch: "arm64",
				Info: info(old, nil, sha("1")), Cache: registryCache(ref("b"), three)},
			"update", ref("b"), "registry", cfgLine(ref("b")), "culibox box setup", "212"},
		{"source build, registry cached",
			Inputs{Configured: defaultImage, CheckOn: true, Arch: "arm64",
				Info: info(old, nil, sha("1")), Cache: registryCache(ref("b"), three)},
			"update", ref("b"), "registry", cfgLine(ref("b")), "culibox box setup", "212"},
		{"explicit pin, registry failed, release",
			Inputs{Configured: ref("9"), Explicit: true, Published: pub, CheckOn: true, Arch: "arm64",
				Info: info(old, []string{ref("9")}), Cache: failed},
			"update", pub, "baked", cfgLine(pub), "culibox box setup", ""},
		{"source build, registry failed",
			Inputs{Configured: defaultImage, CheckOn: true, Arch: "arm64", Info: info(old, nil), Cache: failed},
			"stale-unknown", "", "", "", "", ""},
		{"source build, check off",
			Inputs{Configured: defaultImage, CheckOn: false, Arch: "arm64", Info: info(old, nil)},
			"stale-unknown", "", "", "", "", ""},
		{"info nil",
			Inputs{Configured: defaultImage, Published: pub, CheckOn: true},
			"unknown", pub, "baked", "", "", ""},
		{"not present",
			Inputs{Configured: defaultImage, Published: pub, CheckOn: true, Arch: "arm64", Info: &boximage.ImageInfo{}},
			"missing", pub, "baked", "", "", ""},
		{"MB under 10",
			Inputs{Configured: ref("9"), Explicit: true, CheckOn: true, Arch: "arm64",
				Info:  info(old, nil, sha("1"), sha("2")),
				Cache: registryCache(ref("b"), platform(100e6, 100e6, 4_200_000))},
			"update", ref("b"), "registry", cfgLine(ref("b")), "culibox box setup", "4.2"},
		{"under 0.05 MB is not stated",
			Inputs{Configured: ref("9"), Explicit: true, CheckOn: true, Arch: "arm64",
				Info:  info(old, nil, sha("1"), sha("2")),
				Cache: registryCache(ref("b"), platform(100e6, 100e6, 49_000))},
			"update", ref("b"), "registry", cfgLine(ref("b")), "culibox box setup", ""},
		{"nothing to download",
			Inputs{Configured: ref("9"), Explicit: true, CheckOn: true, Arch: "arm64",
				Info:  info(old, nil, sha("1"), sha("2"), sha("3")),
				Cache: registryCache(ref("b"), three)},
			"update", ref("b"), "registry", cfgLine(ref("b")), "culibox box setup", ""},
		{"no diff ids on the platform",
			Inputs{Configured: ref("9"), Explicit: true, CheckOn: true, Arch: "arm64",
				Info:  info(old, nil, sha("1")),
				Cache: registryCache(ref("b"), boxregistry.Platform{Layers: three.Layers})},
			"update", ref("b"), "registry", cfgLine(ref("b")), "culibox box setup", ""},
		{"no diff ids on the running image",
			Inputs{Configured: ref("9"), Explicit: true, CheckOn: true, Arch: "arm64",
				Info: info(old, nil), Cache: registryCache(ref("b"), three)},
			"update", ref("b"), "registry", cfgLine(ref("b")), "culibox box setup", ""},
		{"arch unknown",
			Inputs{Configured: ref("9"), Explicit: true, CheckOn: true,
				Info:  info(old, nil, sha("1")),
				Cache: registryCache(ref("b"), three)},
			"update", ref("b"), "registry", cfgLine(ref("b")), "culibox box setup", ""},
		{"arch not published",
			Inputs{Configured: ref("9"), Explicit: true, CheckOn: true, Arch: "riscv64",
				Info:  info(old, nil, sha("1")),
				Cache: registryCache(ref("b"), three)},
			"update", ref("b"), "registry", cfgLine(ref("b")), "culibox box setup", ""},
		{"registry answer of another Dockerfile is ignored",
			Inputs{Configured: ref("9"), Explicit: true, Published: pub, CheckOn: true, Arch: "arm64",
				Info: info(old, nil, sha("1")),
				Cache: func() *Cache {
					c := registryCache(ref("b"), three)
					c.Hash = "ffffffffffff"
					return c
				}()},
			"update", pub, "baked", cfgLine(pub), "culibox box setup", ""},
		{"registry answer is not read when no call is allowed",
			Inputs{Configured: defaultImage, Published: pub, CheckOn: true, Arch: "arm64",
				Info: info(old, nil, sha("1")), Cache: registryCache(ref("b"), three)},
			"update", pub, "baked", "", "culibox box setup", ""},
		{"check off ignores the registry answer",
			Inputs{Configured: ref("9"), Explicit: true, Published: pub, CheckOn: false, Arch: "arm64",
				Info: info(old, nil, sha("1")), Cache: registryCache(ref("b"), three)},
			"update", pub, "baked", cfgLine(pub), "culibox box setup", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(tc.in)
			if r.Verdict != tc.verdict || r.Want != tc.want || r.WantSource != tc.src ||
				r.ConfigLine != tc.configLine || r.SetupCmd != tc.setupCmd || r.DownloadMB != tc.mb {
				t.Fatalf("got verdict=%q want=%q src=%q configLine=%q setup=%q mb=%q\nwant verdict=%q want=%q src=%q configLine=%q setup=%q mb=%q",
					r.Verdict, r.Want, r.WantSource, r.ConfigLine, r.SetupCmd, r.DownloadMB,
					tc.verdict, tc.want, tc.src, tc.configLine, tc.setupCmd, tc.mb)
			}
			if r.Configured != tc.in.Configured || r.Explicit != tc.in.Explicit || r.CheckOn != tc.in.CheckOn || r.Info != tc.in.Info {
				t.Fatalf("inputs not carried into the report: %+v", r)
			}
		})
	}
}

func TestEvaluateCarriesCacheFacts(t *testing.T) {
	c := registryCache(ref("b"), platform(1))
	p := c.Registry.Platforms["arm64"]
	p.Created = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	p.ClaudeCode = "2.1.325"
	c.Registry.Platforms["arm64"] = p
	c.RegistryErr = "registry unreachable"
	c.Scan = &Scan{Number: 357, Count: 680}
	r := Evaluate(Inputs{Configured: ref("9"), Explicit: true, CheckOn: true, Arch: "arm64", Info: info("0", nil), Cache: c})
	if !r.WantCreated.Equal(p.Created) || r.WantClaudeCode != "2.1.325" {
		t.Errorf("want facts = %v %q", r.WantCreated, r.WantClaudeCode)
	}
	if !r.CheckedAt.Equal(c.CheckedAt) || r.CheckErr != "registry unreachable" || r.Scan == nil || r.Scan.Number != 357 {
		t.Errorf("cache facts not carried: %+v", r)
	}
	if r := Evaluate(Inputs{Configured: defaultImage, CheckOn: true}); r.Scan != nil || !r.CheckedAt.IsZero() || r.CheckErr != "" {
		t.Errorf("no cache, yet cache facts: %+v", r)
	}
}

// Sizes are the registry's word, already capped per layer; the sum must still
// not wrap however many layers claim the cap.
func TestEvaluateMBDoesNotOverflow(t *testing.T) {
	const capSize = 1 << 40
	var p boxregistry.Platform
	for i := range 9 {
		p.Layers = append(p.Layers, boxregistry.Layer{Size: capSize})
		p.DiffIDs = append(p.DiffIDs, sha(string(rune('1'+i))))
	}
	r := Evaluate(Inputs{Configured: ref("9"), Explicit: true, CheckOn: true, Arch: "arm64",
		Info: info("0", nil, sha("f")), Cache: registryCache(ref("b"), p)})
	if r.DownloadMB != "9895605" { // 9 TiB in MB
		t.Fatalf("DownloadMB = %q", r.DownloadMB)
	}
}

func TestNeedsNetwork(t *testing.T) {
	pub := ref("a")
	for _, tc := range []struct {
		name string
		in   Inputs
		want bool
	}{
		{"release default", Inputs{Published: pub, CheckOn: true}, false},
		{"release explicit", Inputs{Published: pub, Explicit: true, CheckOn: true}, true},
		{"source build", Inputs{CheckOn: true}, true},
		{"check off, source build", Inputs{}, false},
		{"check off, explicit", Inputs{Explicit: true, Published: pub}, false},
	} {
		if got := NeedsNetwork(tc.in); got != tc.want {
			t.Errorf("%s: NeedsNetwork = %v, want %v", tc.name, got, tc.want)
		}
	}
}
