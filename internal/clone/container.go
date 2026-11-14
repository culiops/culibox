package clone

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Labels of the throwaway containers the host starts over a clone. Neither is a
// box label, so claimCheck never reads one as a box.
const (
	LabelCloneFetch   = "io.culiops.agent-box.clonefetch"
	LabelCloneRefresh = "io.culiops.agent-box.clonerefresh"
	LabelCloneList    = "io.culiops.agent-box.clonelist"
)

// removeTimeout bounds the cleanup `docker rm -f`; it runs on its own context
// because the one that ended the run is usually already done.
const removeTimeout = 30 * time.Second

// containerName is a unique, docker-legal name for one throwaway container:
// kind is "clonefetch", "clonerefresh" or "clonelist". The random suffix keeps two runs from
// colliding, and lets a failed run be removed by name.
func containerName(kind, instance string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("clone: crypto/rand: " + err.Error())
	}
	inst := []byte(instance)
	for i, c := range inst {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '.', c == '-':
		default:
			inst[i] = '-'
		}
	}
	return "agent-box-" + kind + "-" + string(inst) + "-" + hex.EncodeToString(b[:])
}

// safeDirEnv is the docker -e options that let the git of a one-shot
// container open exactly these repositories, given by the path git sees in
// the container (the mount target, which is the host path on both OSes).
// git refuses a repository whose files belong to another uid ("detected
// dubious ownership"); on macOS the clone reaches the box VM over virtiofs and
// the container's git reads it as foreign-owned, so every one-shot over a clone
// failed there (#294). Linux never trips it: the clone is the host uid's, as is
// the container's --user.
//
// The setting is command scope (GIT_CONFIG_COUNT/KEY_n/VALUE_n, git >= 2.31),
// which git reads as protected config, so a box-written .git/config can neither
// add nor remove an entry. Each value is one exact path, never "*": a nested or
// planted repository elsewhere in the mounts stays refused. Lifting the check
// is safe here because what it guards against, running a foreign repo's config
// and hooks, is already contained: the container has no network, no
// capabilities, no host credentials, and only the clone (which the box owns
// anyway) and clone_of's .git, read-only.
//
// git drops command-scope config when it starts the other side of a local
// fetch (git-upload-pack on clone_of), so the refresh script gives that process
// its own entry (refreshScript, CULIBOX_SAFE_OF).
func safeDirEnv(repos ...string) []string {
	args := []string{"-e", "GIT_CONFIG_COUNT=" + strconv.Itoa(len(repos))}
	for i, p := range repos {
		n := strconv.Itoa(i)
		args = append(args, "-e", "GIT_CONFIG_KEY_"+n+"=safe.directory", "-e", "GIT_CONFIG_VALUE_"+n+"="+p)
	}
	return args
}

// runContainer runs one named container through d.RunStream. Killing the docker
// CLI does not stop the container, so on any error it force-removes the
// container by name, on a fresh context (the parent may be done). The RunStream
// error is the one returned; a failed removal is logged, since the container
// may still hold a clone mounted read-write (the boot sweep is the backstop).
func runContainer(ctx context.Context, d Docker, name string, args []string, stdout io.Writer) error {
	err := d.RunStream(ctx, args, stdout)
	if err != nil {
		rctx, cancel := context.WithTimeout(context.Background(), removeTimeout)
		defer cancel()
		if rerr := d.Remove(rctx, name); rerr != nil {
			log.Printf("clone: removing container %s: %s", name, sanitize(rerr.Error()))
		}
	}
	return err
}

// Remove runs `docker rm -f name`. A container that is already gone (--rm won
// the race) is not an error.
func (d ExecDocker) Remove(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, d.Bin, d.argv("rm", "-f", name)...)
	errb := &tailBuffer{max: maxDockerStderr}
	cmd.Stdout, cmd.Stderr = io.Discard, errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if strings.Contains(msg, "No such container") {
			return nil
		}
		return fmt.Errorf("docker rm: %w: %s", err, sanitize(tailRunes(msg, maxErrRunes)))
	}
	return nil
}
