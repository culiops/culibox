//go:build dockerit

package gitview

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestSweepRealDocker checks the sweep against a real daemon: the name filter
// and the status filters match, the CreatedAt layout parses, a Created
// leftover goes and a running git container stays (#254).
func TestSweepRealDocker(t *testing.T) {
	d, image := hostileDocker(t)
	leaked, live := newName(), newName()
	t.Cleanup(func() { exec.Command(d.Bin, "rm", "-f", leaked, live).Run() })
	// What a CLI killed between create and start leaves behind; both carry the
	// label every gitview run sets, so only the state tells them apart.
	if out, err := exec.Command(d.Bin, "create", "--name", leaked, "--label", Label+"=1", "--pull=never", "--entrypoint", "true", image).CombinedOutput(); err != nil {
		t.Fatalf("docker create: %v: %s", err, out)
	}
	if out, err := exec.Command(d.Bin, "run", "-d", "--name", live, "--label", Label+"=1", "--pull=never", "--entrypoint", "sleep", image, "120").CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	// An hour from now, both are past sweepGrace.
	r := &Runner{Docker: d, Image: image, Now: func() time.Time { return time.Now().Add(time.Hour) }}
	n, err := r.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if exec.Command(d.Bin, "container", "inspect", leaked).Run() == nil {
		t.Errorf("the Created leftover %s is still there", leaked)
	}
	if err := exec.Command(d.Bin, "container", "inspect", live).Run(); err != nil {
		t.Errorf("the running container %s was removed", live)
	}
	if n < 1 {
		t.Errorf("Sweep removed %d", n)
	}
}
