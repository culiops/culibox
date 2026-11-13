package upgradebackup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func restoreRoundTrip(t *testing.T, r string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(r, "store", "values"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, r, "profiles.json", `{"orig":1}`)
	writeFile(t, r, filepath.Join("store", "values", "a.age"), "orig-secret")
	writeFile(t, r, StampName, "v0 old\n")

	res, err := Run(opts(r, "v1"))
	if err != nil || !res.Taken {
		t.Fatalf("backup: %+v %v", res, err)
	}
	writeFile(t, r, "profiles.json", `{"changed":2}`)
	writeFile(t, r, filepath.Join("store", "values", "a.age"), "changed")
	writeFile(t, r, "newfile", "keep me")

	out, err := exec.Command("sh", "-c", RestoreCommand(r, res.Dir)).CombinedOutput()
	if err != nil {
		t.Fatalf("restore command: %v\n%s\ncmd: %s", err, out, RestoreCommand(r, res.Dir))
	}
	for name, want := range map[string]string{
		"profiles.json": `{"orig":1}`,
		filepath.Join("store", "values", "a.age"): "orig-secret",
		"newfile": "keep me",
		StampName: "v0 old\n",
	} {
		b, err := os.ReadFile(filepath.Join(r, name))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v; want %q", name, b, err, want)
		}
	}
}

func TestRestoreCommandRoundTrip(t *testing.T) {
	restoreRoundTrip(t, t.TempDir())
}

func TestRestoreCommandRoundTripSpaceAndQuote(t *testing.T) {
	r := filepath.Join(t.TempDir(), "my data's root")
	if err := os.MkdirAll(r, 0o700); err != nil {
		t.Fatal(err)
	}
	restoreRoundTrip(t, r)
}

func TestRestoreCommandText(t *testing.T) {
	got := RestoreCommand("/d/r", "/d/r/backups/b")
	want := `rm -f '/d/r'/last-version && cp -a '/d/r/backups/b'/. '/d/r'/ && rm -f '/d/r'/audit.db-wal '/d/r'/audit.db-shm`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if strings.Contains(got, "$") {
		t.Fatalf("shell variable in %s", got)
	}
}
