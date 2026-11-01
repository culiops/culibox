package box

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	idRunning = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	idStopped = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func runMountSources(t *testing.T, outputs map[string]string, codes map[string]int) ([]string, error, string) {
	t.Helper()
	bin, log := fakeDocker(t, outputs, codes)
	got, err := CLIClient{Bin: bin}.MountSources(context.Background())
	raw, _ := os.ReadFile(log)
	return got, err, string(raw)
}

func TestMountSourcesCoversEveryContainerInTheStore(t *testing.T) {
	got, err, argv := runMountSources(t, map[string]string{
		"ps": idRunning + "\n" + idStopped,
		"inspect": idRunning + ` [{"Type":"bind","Source":"/r/a"},{"Type":"tmpfs","Source":""}]` + "\n" +
			idStopped + ` [{"Type":"bind","Source":"/r/with space/b"},{"Type":"volume","Source":"/var/lib/docker/volumes/v/_data"}]`,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/r/a", "/r/with space/b", "/var/lib/docker/volumes/v/_data"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("sources = %q, want %q", got, want)
	}
	// Without -a a stopped box would read as gone. A label filter would hide the
	// other instance's boxes, which share this docker store.
	psLine := strings.SplitN(argv, "\n", 2)[0]
	if !strings.Contains(psLine, "ps -a") {
		t.Fatalf("ps must list stopped containers too: %q", psLine)
	}
	if strings.Contains(psLine, "--filter") {
		t.Fatalf("ps must not filter: %q", psLine)
	}
}

// An empty store is a real answer, and only when docker itself says so.
func TestMountSourcesEmptyStoreIsNotAnError(t *testing.T) {
	got, err, argv := runMountSources(t, map[string]string{"ps": ""}, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %q, %v", got, err)
	}
	if strings.Contains(argv, "inspect") {
		t.Fatalf("inspect with no ids prints usage and fails: %q", argv)
	}
}

func TestMountSourcesFailsClosed(t *testing.T) {
	line := func(mounts string) string { return idRunning + " " + mounts }
	for name, tc := range map[string]struct {
		outputs map[string]string
		codes   map[string]int
	}{
		"ps fails":           {map[string]string{"ps": ""}, map[string]int{"ps": 1}},
		"ps prints garbage":  {map[string]string{"ps": "WARNING: something"}, nil},
		"inspect fails":      {map[string]string{"ps": idRunning, "inspect": ""}, map[string]int{"inspect": 1}},
		"inspect is empty":   {map[string]string{"ps": idRunning, "inspect": ""}, nil},
		"not json":           {map[string]string{"ps": idRunning, "inspect": line("nonsense")}, nil},
		"mounts null":        {map[string]string{"ps": idRunning, "inspect": line("null")}, nil},
		"line without json":  {map[string]string{"ps": idRunning, "inspect": idRunning}, nil},
		"bind without path":  {map[string]string{"ps": idRunning, "inspect": line(`[{"Type":"bind","Source":""}]`)}, nil},
		"bind relative path": {map[string]string{"ps": idRunning, "inspect": line(`[{"Type":"bind","Source":"r/a"}]`)}, nil},
		"mount without type": {map[string]string{"ps": idRunning, "inspect": line(`[{"Source":"/r/a"}]`)}, nil},
		"container missing": {map[string]string{"ps": idRunning + "\n" + idStopped,
			"inspect": line(`[]`)}, nil},
		"answer for another container": {map[string]string{"ps": idRunning,
			"inspect": idStopped + ` []`}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got, err, _ := runMountSources(t, tc.outputs, tc.codes)
			if err == nil {
				t.Fatalf("no error, sources = %q: an unreadable answer must not read as no references", got)
			}
		})
	}
}
