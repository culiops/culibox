// Package dockerfake is a stateful /bin/sh docker CLI for tests of the code that
// lists, pulls and removes images. It keeps its image store, containers and
// argv log in files under a temp dir, so a test can arrange a store, run the
// code, and read what is left. It is imported only by tests.
//
// It answers just the verbs the box image code uses: version, image ls,
// image inspect, container ls, container inspect, pull, build, tag and image rm. Like
// docker, rm refuses an image a container uses and an image that has several
// names when it is given the bare ID, and this fake also refuses -f, which the
// code under test must never pass.
package dockerfake

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// None is how docker shows a missing repository, tag or digest.
const None = "<none>"

// Fake is one fake docker and its store.
type Fake struct {
	t   *testing.T
	Bin string
	dir string
}

// Ref is one name of an image, a row of `docker image ls --digests`. A pulled
// image has a Digest; a locally built one does not.
type Ref struct{ Repo, Tag, Digest string }

// Image is one image in the store.
type Image struct {
	ID      string // the full ID, "sha256:…"
	Refs    []Ref  // its names; none = a dangling image
	Size    int64  // bytes, as image inspect reports
	Labeled bool   // carries the box Dockerfile label
	Arch    string // default arm64
	Hash    string // the Dockerfile label value, default "h0"
}

// New writes the fake docker script into a fresh temp dir. The daemon is arm64.
func New(t *testing.T) *Fake {
	t.Helper()
	dir := t.TempDir()
	f := &Fake{t: t, Bin: filepath.Join(dir, "docker"), dir: dir}
	for _, n := range []string{"images", "labeled", "meta", "containers", "pull", "build", "failrm", "noop", "log"} {
		f.write(n, "")
	}
	f.write("arch", "arm64\n")
	if err := os.WriteFile(f.Bin, []byte(script(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *Fake) write(name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *Fake) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(b)
}

func (f *Fake) appendTo(name, line string) {
	f.t.Helper()
	fh, err := os.OpenFile(filepath.Join(f.dir, name), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		f.t.Fatal(err)
	}
	defer fh.Close()
	fmt.Fprintln(fh, line)
}

func orNone(s string) string {
	if s == "" {
		return None
	}
	return s
}

// Add puts the image in the store.
func (f *Fake) Add(im Image) {
	f.t.Helper()
	if im.Arch == "" {
		im.Arch = "arm64"
	}
	if im.Hash == "" {
		im.Hash = "h0"
	}
	refs := im.Refs
	if len(refs) == 0 {
		refs = []Ref{{}}
	}
	for _, r := range refs {
		f.appendTo("images", strings.Join([]string{im.ID, orNone(r.Repo), orNone(r.Tag), orNone(r.Digest)}, "\t"))
	}
	f.appendTo("meta", fmt.Sprintf("%s\t%d\t%s\t%s", im.ID, im.Size, im.Arch, im.Hash))
	if im.Labeled {
		f.appendTo("labeled", im.ID)
	}
}

// Container records a container (any state) that uses the image.
func (f *Fake) Container(id, imageID string) { f.appendTo("containers", id+"\t"+imageID) }

// PullAdds makes `docker pull ref` put im (its first Ref) in the store.
func (f *Fake) PullAdds(ref string, im Image) {
	f.t.Helper()
	r := im.Refs[0]
	labeled := "0"
	if im.Labeled {
		labeled = "1"
	}
	if im.Arch == "" {
		im.Arch = "arm64"
	}
	if im.Hash == "" {
		im.Hash = "h0"
	}
	f.appendTo("pull", strings.Join([]string{ref, im.ID, fmt.Sprint(im.Size), orNone(r.Repo), orNone(r.Tag), orNone(r.Digest), labeled, im.Arch, im.Hash}, "\t"))
}

// BuildAdds makes the next `docker build … -t name …` produce im: every -t name
// moves to it (an old holder of a name is left unnamed when that was its last).
// im.Refs[0].Digest, when set, is the digest every new name shows, as on the
// containerd image store, which shows one on local builds too.
func (f *Fake) BuildAdds(im Image) {
	f.t.Helper()
	labeled := "0"
	if im.Labeled {
		labeled = "1"
	}
	digest, hash := None, im.Hash
	if len(im.Refs) > 0 && im.Refs[0].Digest != "" {
		digest = im.Refs[0].Digest
	}
	if hash == "" {
		hash = "h0"
	}
	f.appendTo("build", strings.Join([]string{im.ID, fmt.Sprint(im.Size), labeled, digest, hash}, "\t"))
}

// FailRm makes removing the ref (or ID) fail as a daemon error.
func (f *Fake) FailRm(ref string) { f.appendTo("failrm", ref) }

// RmNoop makes removing the ref (or ID) report success and change nothing.
func (f *Fake) RmNoop(ref string) { f.appendTo("noop", ref) }

// Has reports whether the store holds an image with this ID.
func (f *Fake) Has(id string) bool {
	for _, l := range strings.Split(f.read("images"), "\n") {
		if strings.HasPrefix(l, id+"\t") {
			return true
		}
	}
	return false
}

// Log is every argv the fake saw, one per line.
func (f *Fake) Log() string { return f.read("log") }

// Count is how many logged argvs start with prefix.
func (f *Fake) Count(prefix string) int {
	n := 0
	for _, l := range strings.Split(f.Log(), "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func script(dir string) string {
	return `#!/bin/sh
d=` + dir + `
printf '%s\n' "$*" >> "$d/log"
while [ "$1" = --host ] || [ "$1" = --config ] || [ "$1" = --context ]; do shift 2; done
# id_of REF: the ID a name (repo:tag or repo@digest) points at.
id_of() {
  awk -F'\t' -v r="$1" '
    ($2 ":" $3)==r {print $1; exit}
    ($2 "@" $4)==r {print $1; exit}
    ' "$d/images"
}
# resolve REF: a name or a full ID -> the ID, "" when the store has none.
resolve() {
  id=$(id_of "$1")
  [ -z "$id" ] && id=$(awk -F'\t' -v r="$1" '$1==r {print $1; exit}' "$d/images")
  echo "$id"
}
case "$1 $2" in
"image ls")
  case "$*" in
  *--filter*) sort -u "$d/labeled" ;;
  *) cat "$d/images" ;;
  esac
  exit 0 ;;
"image inspect")
  shift 2
  fmt=""; if [ "$1" = --format ]; then fmt=$2; shift 2; fi
  for ref in "$@"; do
    case "$ref" in *" "*) echo "Error response from daemon: invalid reference format" >&2; exit 1 ;; esac
    id=$(resolve "$ref")
    if [ -z "$id" ]; then echo "Error: No such image: $ref" >&2; exit 1; fi
    case "$fmt" in
    *Architecture*) awk -F'\t' -v i="$id" '$1==i {a=$3; h=$4} END {if (h=="") h="<no value>"; print a "|" h}' "$d/meta" ;;
    *Size*) awk -F'\t' -v i="$id" '$1==i {print $1 "\t" $2}' "$d/meta" ;;
    *) echo "$id" ;;
    esac
  done
  exit 0 ;;
"container ls") cut -f1 "$d/containers"; exit 0 ;;
"container inspect")
  shift 2
  [ "$1" = --format ] && shift 2
  for c in "$@"; do
    img=$(awk -F'\t' -v c="$c" '$1==c {print $2; exit}' "$d/containers")
    if [ -z "$img" ]; then echo "Error: No such container: $c" >&2; exit 1; fi
    echo "$img"
  done
  exit 0 ;;
esac
case "$1" in
version) cat "$d/arch"; exit 0 ;;
tag)
  # docker tag SRC DST: DST moves to SRC's image; an old holder of DST is left
  # unnamed (dangling) when that was its last name.
  id=$(resolve "$2")
  if [ -z "$id" ]; then echo "Error response from daemon: No such image: $2" >&2; exit 1; fi
  repo=${3%:*}; tag=${3##*:}
  old=$(awk -F'\t' -v r="$repo" -v g="$tag" '$2==r && $3==g {print $1}' "$d/images")
  awk -F'\t' -v r="$repo" -v g="$tag" '!($2==r && $3==g)' "$d/images" > "$d/images.new"; mv "$d/images.new" "$d/images"
  for o in $old; do
    awk -F'\t' -v i="$o" '$1==i {f=1} END {exit !f}' "$d/images" || printf '%s\t<none>\t<none>\t<none>\n' "$o" >> "$d/images"
  done
  printf '%s\t%s\t%s\t<none>\n' "$id" "$repo" "$tag" >> "$d/images"
  exit 0 ;;
pull)
  ref=""; for a; do ref=$a; done
  row=$(awk -F'\t' -v r="$ref" '$1==r {print; exit}' "$d/pull")
  if [ -z "$row" ]; then echo "Error response from daemon: denied" >&2; exit 1; fi
  echo "$row" | awk -F'\t' -v OFS='\t' '{print $2, $4, $5, $6 >> "'"$d"'/images"; print $2, $3, $8, $9 >> "'"$d"'/meta"; if ($7=="1") print $2 >> "'"$d"'/labeled"}'
  echo "pulled $ref"
  exit 0 ;;
build)
  cat >/dev/null
  row=$(head -n 1 "$d/build")
  if [ -z "$row" ]; then echo "fake docker: no image arranged for build" >&2; exit 1; fi
  sed -i 1d "$d/build"
  id=$(echo "$row" | cut -f1); size=$(echo "$row" | cut -f2); lab=$(echo "$row" | cut -f3); dig=$(echo "$row" | cut -f4); hash=$(echo "$row" | cut -f5)
  prev=""
  for a; do
    if [ "$prev" = -t ]; then
      repo=${a%:*}; tag=${a##*:}
      old=$(awk -F'\t' -v r="$repo" -v g="$tag" '$2==r && $3==g {print $1}' "$d/images")
      awk -F'\t' -v r="$repo" -v g="$tag" '!($2==r && $3==g)' "$d/images" > "$d/images.new"; mv "$d/images.new" "$d/images"
      # A name that moved leaves its old image unnamed (dangling) when it was the last.
      for o in $old; do
        awk -F'\t' -v i="$o" '$1==i {f=1} END {exit !f}' "$d/images" || printf '%s\t<none>\t<none>\t<none>\n' "$o" >> "$d/images"
      done
      printf '%s\t%s\t%s\t%s\n' "$id" "$repo" "$tag" "$dig" >> "$d/images"
    fi
    prev=$a
  done
  printf '%s\t%s\tarm64\t%s\n' "$id" "$size" "$hash" >> "$d/meta"
  [ "$lab" = 1 ] && echo "$id" >> "$d/labeled"
  echo "built $id"
  exit 0 ;;
image)
  [ "$2" = rm ] || { echo "fake docker: unsupported: $*" >&2; exit 1; }
  shift 2
  for a in "$@"; do
    case "$a" in -f|--force) echo "fake docker: rm must not be forced" >&2; exit 2 ;; esac
  done
  rc=0
  for a in "$@"; do
    if grep -qxF -- "$a" "$d/failrm"; then echo "Error response from daemon: simulated failure removing $a" >&2; rc=1; continue; fi
    if grep -qxF -- "$a" "$d/noop"; then echo "Deleted: $a"; continue; fi
    id=$(id_of "$a"); byid=0
    if [ -z "$id" ]; then id=$(awk -F'\t' -v r="$a" '$1==r {print $1; exit}' "$d/images"); byid=1; fi
    if [ -z "$id" ]; then echo "Error response from daemon: No such image: $a" >&2; rc=1; continue; fi
    # As docker does: a name that is not the image's last is only untagged, with
    # no container check; the last name (or a bare ID) deletes the image, which a
    # container using it refuses; a bare ID with several names is refused.
    named=$(awk -F'\t' -v i="$id" '$1==i && $2!="<none>"' "$d/images" | wc -l)
    if [ "$byid" = 1 ] && [ "$named" -gt 1 ]; then
      echo "Error response from daemon: conflict: unable to delete $a (must be forced) - image is referenced in multiple repositories" >&2; rc=1; continue
    fi
    if [ "$byid" = 0 ] && [ "$named" -gt 1 ]; then
      awk -F'\t' -v r="$a" '!(($2 ":" $3)==r || ($2 "@" $4)==r)' "$d/images" > "$d/images.new"; mv "$d/images.new" "$d/images"
      echo "Untagged: $a"; continue
    fi
    if awk -F'\t' -v i="$id" '$2==i {f=1} END {exit !f}' "$d/containers"; then
      echo "Error response from daemon: conflict: unable to delete $a (cannot be forced) - image is being used by a container" >&2; rc=1; continue
    fi
    awk -F'\t' -v i="$id" '$1!=i' "$d/images" > "$d/images.new"; mv "$d/images.new" "$d/images"
    echo "Deleted: $id"
  done
  exit $rc ;;
esac
echo "fake docker: unsupported: $*" >&2
exit 1
`
}
