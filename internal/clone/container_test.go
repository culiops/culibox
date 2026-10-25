package clone

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestContainerName(t *testing.T) {
	n := containerName("clonerefresh", "DEV")
	if !regexp.MustCompile(`^agent-box-clonerefresh-DEV-[0-9a-f]{16}$`).MatchString(n) {
		t.Errorf("name = %q", n)
	}
	odd := containerName("clonefetch", "a b/é")
	if !regexp.MustCompile(`^agent-box-clonefetch-[A-Za-z0-9_.-]+-[0-9a-f]{16}$`).MatchString(odd) {
		t.Errorf("name = %q has bytes outside [A-Za-z0-9_.-]", odd)
	}
	if n == containerName("clonerefresh", "DEV") {
		t.Error("two names are equal; they must be unique")
	}
}

func TestRunContainerRemovesOnFailure(t *testing.T) {
	d := &fakeDocker{t: t, hang: true}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := runContainer(ctx, d, "boxname", []string{"run"}, io.Discard)
	if err == nil {
		t.Fatal("want the RunStream error")
	}
	if !slices.Equal(d.removed, []string{"boxname"}) {
		t.Errorf("removed = %q, want [boxname]", d.removed)
	}
}

// removeCtxDocker records whether the context handed to Remove was usable.
type removeCtxDocker struct {
	fakeDocker
	removeErr error
}

func (r *removeCtxDocker) RunStream(ctx context.Context, _ []string, _ io.Writer) error {
	<-ctx.Done()
	return ctx.Err()
}

func (r *removeCtxDocker) Remove(ctx context.Context, name string) error {
	r.removeErr = ctx.Err()
	return r.fakeDocker.Remove(ctx, name)
}

func TestRunContainerRemovesWithAFreshContext(t *testing.T) {
	d := &removeCtxDocker{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := runContainer(ctx, d, "n", nil, io.Discard); err == nil {
		t.Fatal("want an error")
	}
	if ctx.Err() == nil {
		t.Fatal("the parent context should be done")
	}
	if d.removeErr != nil {
		t.Errorf("Remove got a done context: %v", d.removeErr)
	}
}

func TestRunContainerNoRemoveOnSuccess(t *testing.T) {
	d := &okDocker{}
	if err := runContainer(context.Background(), d, "n", nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(d.removed) != 0 {
		t.Errorf("removed = %q, want none", d.removed)
	}
}

type okDocker struct{ removed []string }

func (o *okDocker) RunStream(context.Context, []string, io.Writer) error { return nil }
func (o *okDocker) Remove(_ context.Context, n string) error {
	o.removed = append(o.removed, n)
	return nil
}

func TestExecDockerRemove(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args")
	bin := writeScript(t, `printf '%s' "$*" > `+log)
	if err := (ExecDocker{Bin: bin}).Remove(context.Background(), "n1"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(log)
	if string(got) != "rm -f n1" {
		t.Errorf("argv = %q, want %q", got, "rm -f n1")
	}

	gone := writeScript(t, `echo "Error response from daemon: No such container: n1" >&2; exit 1`)
	if err := (ExecDocker{Bin: gone}).Remove(context.Background(), "n1"); err != nil {
		t.Errorf("a missing container is not an error, got %v", err)
	}

	bad := writeScript(t, `echo "permission denied" >&2; exit 1`)
	if err := (ExecDocker{Bin: bad}).Remove(context.Background(), "n1"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v, want one carrying the docker answer", err)
	}
}

func TestFetchRemovesBundleContainerOnTimeout(t *testing.T) {
	x := newFetchFx(t)
	x.dock.hang = true
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Fetch(ctx, x.rec, x.dock, x.req); err == nil {
		t.Fatal("want an error")
	}
	i := slices.Index(x.dock.args, "--name")
	if i < 0 || !strings.HasPrefix(x.dock.args[i+1], "agent-box-clonefetch-T-") {
		t.Fatalf("argv has no --name: %q", x.dock.args)
	}
	if !slices.Equal(x.dock.removed, []string{x.dock.args[i+1]}) {
		t.Errorf("removed = %q, want [%s]", x.dock.removed, x.dock.args[i+1])
	}
	if !slices.Contains(x.dock.args, "--init") {
		t.Error("argv has no --init")
	}
	x.tempEmpty()
}
