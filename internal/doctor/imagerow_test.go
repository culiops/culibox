package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/boximagecheck"
	"github.com/culiops/culibox/internal/boxvm"
	"github.com/culiops/culibox/internal/config"
)

const wantRef = "ghcr.io/culiops/culibox@sha256:81be000000000000000000000000000000000000000000000000000000000000"

var builtOn = time.Date(2026, 11, 6, 13, 0, 0, 0, time.UTC)

func rowsText(rs []Result) string { return detail(rs) }

func TestImageUpdateRows(t *testing.T) {
	info := &boximage.ImageInfo{Present: true, ClaudeCode: "2.1.292", Created: builtOn}
	for _, tc := range []struct {
		name string
		r    *boximagecheck.Report
		want []Result
	}{
		{"nil report", nil, nil},
		{"missing", &boximagecheck.Report{Verdict: boximagecheck.Missing}, nil},
		{"unknown", &boximagecheck.Report{Verdict: boximagecheck.Unknown}, nil},
		{"current", &boximagecheck.Report{Verdict: boximagecheck.Current, Configured: "culiops/box:x", Info: info},
			[]Result{{Pass, "box image update", "current: culiops/box:x, Claude Code 2.1.292, built 2026-11-06"}}},
		{"current without facts", &boximagecheck.Report{Verdict: boximagecheck.Current, Configured: "culiops/box:x", Info: &boximage.ImageInfo{Present: true}},
			[]Result{{Pass, "box image update", "current: culiops/box:x"}}},
		{"current-local", &boximagecheck.Report{Verdict: boximagecheck.CurrentLocal, Configured: "culiops/box:x", Info: info},
			[]Result{{Pass, "box image update", "current (built here from this version's Dockerfile): culiops/box:x, Claude Code 2.1.292, built 2026-11-06"}}},
		{"update with config line", &boximagecheck.Report{Verdict: boximagecheck.Update, Want: wantRef, WantClaudeCode: "2.1.311",
			WantCreated: time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC), DownloadMB: "212",
			ConfigLine: `"box_image": "` + wantRef + `"`, SetupCmd: "culibox box setup"},
			[]Result{{Warn, "box image update", "an image for this version exists: " + wantRef + " (Claude Code 2.1.311, built 2026-11-20); about 212 MB to download; set \"box_image\": \"" + wantRef + "\" in the config, then run `culibox box setup`"}}},
		{"update release default", &boximagecheck.Report{Verdict: boximagecheck.Update, Want: wantRef, SetupCmd: "culibox box setup"},
			[]Result{{Warn, "box image update", "an image for this version exists: " + wantRef + "; run `culibox box setup`"}}},
		{"stale unknown with error", &boximagecheck.Report{Verdict: boximagecheck.StaleUnknown, CheckOn: true, CheckErr: "registry unreachable"},
			[]Result{{Warn, "box image update", "built from an older Dockerfile than this CuliBox; whether CI published this version's image is unknown (registry unreachable)"}}},
		{"stale unknown not checked", &boximagecheck.Report{Verdict: boximagecheck.StaleUnknown, CheckOn: true},
			[]Result{{Warn, "box image update", "built from an older Dockerfile than this CuliBox; whether CI published this version's image is unknown (not checked yet)"}}},
		{"stale unknown check off", &boximagecheck.Report{Verdict: boximagecheck.StaleUnknown},
			[]Result{{Warn, "box image update", "built from an older Dockerfile than this CuliBox; whether CI published this version's image is unknown (check off: image_check is false)"}}},
		{"scan with findings", &boximagecheck.Report{Verdict: boximagecheck.Unknown, Scan: &boximagecheck.Scan{Number: 357, Count: 680}},
			[]Result{{Warn, "box image scan", "680 fixable HIGH/CRITICAL in the published images (https://github.com/culiops/culibox/issues/357)"}}},
		{"scan clean", &boximagecheck.Report{Verdict: boximagecheck.Missing, Scan: &boximagecheck.Scan{Number: 0, Count: 0}},
			[]Result{{Pass, "box image scan", "no fixable HIGH/CRITICAL in the published images"}}},
		{"update and scan", &boximagecheck.Report{Verdict: boximagecheck.Update, Want: wantRef, SetupCmd: "culibox box setup", Scan: &boximagecheck.Scan{Number: 357, Count: 3}},
			[]Result{
				{Warn, "box image update", "an image for this version exists: " + wantRef + "; run `culibox box setup`"},
				{Warn, "box image scan", "3 fixable HIGH/CRITICAL in the published images (https://github.com/culiops/culibox/issues/357)"},
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := imageUpdateRows(tc.r)
			if len(got) != len(tc.want) {
				t.Fatalf("rows:\n%s\nwant:\n%s", rowsText(got), rowsText(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("row %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestBoxTierImageRowsPlacement(t *testing.T) {
	rep := &boximagecheck.Report{Verdict: boximagecheck.Update, Want: wantRef, SetupCmd: "culibox box setup"}
	idx := func(rs []Result, check string) int {
		for i, r := range rs {
			if r.Check == check {
				return i
			}
		}
		return -1
	}
	// Linux: right after the runtime rows.
	lf := boxFacts{RuntimeRan: true, Runtime: "runc", Stopped: []string{"b1"}, ImageReport: rep}
	rows := boxTierResults("linux", lf, tokenOK)
	if a, b := idx(rows, "isolation runtime"), idx(rows, "box image update"); a < 0 || b != a+1 {
		t.Errorf("linux: update row at %d, runtime row at %d:\n%s", b, a, detail(rows))
	}
	lf.ImageReport = nil
	if strings.Contains(detail(boxTierResults("linux", lf, tokenOK)), "box image update") {
		t.Error("linux: a nil report has a row")
	}
	// Darwin: right after the existing "box image" row.
	df := ok()
	df.RuntimeRan, df.Runtime, df.ImageReport = true, "runc", rep
	rows = boxTierResults("darwin", df, tokenOK)
	if a, b := idx(rows, "box image"), idx(rows, "box image update"); a < 0 || b != a+1 {
		t.Errorf("darwin: update row at %d, image row at %d:\n%s", b, a, detail(rows))
	}
	// With no report the output is what it was.
	df.ImageReport = nil
	if strings.Contains(detail(boxTierResults("darwin", df, tokenOK)), "box image update") {
		t.Error("darwin: a nil report has a row")
	}
}

// imageFakeDocker answers `docker version` with an arch and `docker image inspect`
// with a Describe-able image.
func imageFakeDocker(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	script := `#!/bin/sh
case "$*" in
*"version --format"*) echo amd64; exit 0;;
*"image inspect"*) echo '{"Id":"sha256:` + strings.Repeat("a", 64) + `","Architecture":"amd64","Created":"2026-11-06T13:00:00Z","Config":{"Labels":{"io.culiops.agent-box.claude-code":"2.1.292"}}}'; exit 0;;
*" info"*|info*) echo '{"runc":{}}'; exit 0;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestGatherReadsTheRunningImageOnLinuxOnlyWhenAsked(t *testing.T) {
	bin := imageFakeDocker(t)
	c := config.Config{DockerBin: bin}
	f := gatherFacts(c, t.TempDir(), t.TempDir(), "linux", nil, true)
	if f.ImageInfo == nil || !f.ImageInfo.Present || f.ImageInfo.ClaudeCode != "2.1.292" || f.ServerArch != "amd64" {
		t.Fatalf("facts: info=%+v arch=%q", f.ImageInfo, f.ServerArch)
	}
	if f := gatherFacts(c, t.TempDir(), t.TempDir(), "linux", nil, false); f.ImageInfo != nil {
		t.Errorf("not asked, yet the image was described: %+v", f.ImageInfo)
	}
	if f := gatherBoxFacts(c, t.TempDir(), t.TempDir(), "linux", nil); f.ImageInfo != nil {
		t.Errorf("gatherBoxFacts describes the image: %+v", f.ImageInfo)
	}
	// A daemon that does not answer is never asked about the image.
	down := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(down, []byte("#!/bin/sh\necho 'Cannot connect to the Docker daemon. Is the docker daemon running?' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if f := gatherFacts(config.Config{DockerBin: down}, t.TempDir(), t.TempDir(), "linux", nil, true); f.DaemonErr == nil || f.ImageInfo != nil {
		t.Errorf("down daemon: err=%v info=%+v", f.DaemonErr, f.ImageInfo)
	}
}

// failingClient fails the test on any request.
func failingClient(t *testing.T) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected network call: %s", r.URL)
		return nil, context.Canceled
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func useChecker(t *testing.T, k boximagecheck.Checker) {
	t.Helper()
	old := imageChecker
	imageChecker = func() boximagecheck.Checker { return k }
	t.Cleanup(func() { imageChecker = old })
}

func TestImageReportNeverTouchesTheNetworkWithoutLiveCheck(t *testing.T) {
	useChecker(t, boximagecheck.Checker{Client: failingClient(t), Registry: "http://x.invalid", GitHubAPI: "http://x.invalid"})
	dir := t.TempDir()
	// A source build with the check on: the network is allowed, but not asked.
	f := boxFacts{ServerArch: "amd64", ImageInfo: &boximage.ImageInfo{Present: true}}
	r := imageReport(Input{Config: config.Config{BoxImage: "culiops/box:x"}}, dir, f)
	if r == nil || r.Verdict != boximagecheck.StaleUnknown {
		t.Fatalf("report = %+v, want stale-unknown from no cache", r)
	}
	if _, err := os.Stat(filepath.Join(dir, boximagecheck.CacheFile)); err == nil {
		t.Error("a doctor without LiveImageCheck wrote the cache")
	}
}

func TestImageReportLiveCheckRunsAndSurvivesAFailedSave(t *testing.T) {
	calls := 0
	useChecker(t, boximagecheck.Checker{
		Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return nil, context.DeadlineExceeded
		})},
		Registry: "https://ghcr.io", GitHubAPI: "https://api.github.com"})
	// The data dir does not exist yet, so the save fails; the answer still shows.
	dir := filepath.Join(t.TempDir(), "not-yet")
	f := boxFacts{ServerArch: "amd64", ImageInfo: &boximage.ImageInfo{Present: true}}
	r := imageReport(Input{Config: config.Config{BoxImage: "culiops/box:x"}, LiveImageCheck: true}, dir, f)
	if calls == 0 {
		t.Fatal("LiveImageCheck did not ask")
	}
	if r == nil || r.Verdict != boximagecheck.StaleUnknown || r.CheckErr == "" {
		t.Fatalf("report = %+v, want stale-unknown with the failure class", r)
	}
	// The row says the class, never the transport's text.
	got := detail(imageUpdateRows(r))
	if strings.Contains(got, "deadline") || strings.Contains(got, "ghcr.io") {
		t.Errorf("row carries raw error text: %s", got)
	}
}

func TestImageReportOffMeansNoCall(t *testing.T) {
	useChecker(t, boximagecheck.Checker{Client: failingClient(t), Registry: "http://x.invalid", GitHubAPI: "http://x.invalid"})
	off := false
	f := boxFacts{ServerArch: "amd64", ImageInfo: &boximage.ImageInfo{Present: true}}
	r := imageReport(Input{Config: config.Config{BoxImage: "culiops/box:x", ImageCheck: &off}, LiveImageCheck: true}, t.TempDir(), f)
	if r == nil || r.CheckOn {
		t.Fatalf("report = %+v", r)
	}
}

// On macOS the image is described only when the VM runs and its image was read.
func TestGatherReadsTheRunningImageOnMacOSWhenTheVMRuns(t *testing.T) {
	dockerBin := imageFakeDocker(t)
	dir := t.TempDir()
	colima := filepath.Join(dir, "colima")
	running := "status) echo '{\"driver\":\"vz\",\"mount_type\":\"virtiofs\"}' ;;\n"
	for name, status := range map[string]string{"running": running, "stopped": "status) exit 1 ;;\n"} {
		script := "#!/bin/sh\ncase \"$1\" in\nversion) echo 'colima version 0.8.1' ;;\n" + status + "ssh) exit 0 ;;\nesac\nexit 0\n"
		if err := os.WriteFile(colima, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		data, err := os.MkdirTemp("/tmp", "abdoc-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(data) })
		c := config.Config{DockerBin: dockerBin, ColimaBin: colima}
		vm := boxvm.NewVM(boxvm.ConfigFor(c, data, t.TempDir(), colima, dockerBin, 4, 8), boxvm.ExecRunner{})
		vc := vm.Config()
		y, err := vc.YAML()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(vc.ProfileDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(vc.YAMLPath(), y, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(y)
		if err := os.WriteFile(vc.StampPath(), []byte(hex.EncodeToString(sum[:])+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f := gatherFacts(c, data, t.TempDir(), "darwin", nil, true)
		if name == "running" && (f.ImageInfo == nil || f.ImageInfo.ClaudeCode != "2.1.292") {
			t.Errorf("running VM: image info = %+v (status %+v, err %v)", f.ImageInfo, f.Status, f.StatusErr)
		}
		if name == "stopped" && f.ImageInfo != nil {
			t.Errorf("stopped VM was asked about its image: %+v", f.ImageInfo)
		}
	}
}
