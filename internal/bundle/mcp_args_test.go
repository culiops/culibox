package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageProberCarriesTheGlobalArgs(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\necho NOMCP\necho 'OK node'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got := ImageProber{Docker: bin, Args: []string{"--host", "unix:///v/s"}, Image: "img"}.Resolves([]string{"node"})
	if !got["node"] {
		t.Fatalf("probe output not parsed: %v", got)
	}
	b, _ := os.ReadFile(log)
	if !strings.HasPrefix(string(b), "--host unix:///v/s run --rm --pull=never --network none --entrypoint sh img -c ") {
		t.Fatalf("probe must carry the endpoint flags: %q", b)
	}
}
