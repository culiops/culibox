package box

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// withMountInfo stands in for the host's mount table (#308): bind mounts need
// privileges a test does not have, so the table says which host folder is
// mounted from where, and same says which pairs of paths are one directory on
// disk (the sameHostDir seam).
func withMountInfo(t *testing.T, table string, err error, same func(a, b string) bool) {
	t.Helper()
	oldInfo, oldSame := hostMountInfo, sameHostDir
	if table == "" && err == nil {
		hostMountInfo = nil
	} else {
		hostMountInfo = func() ([]byte, error) { return []byte(table), err }
	}
	if same != nil {
		sameHostDir = func(a, b string) bool { return same(a, b) || oldSame(a, b) }
	}
	t.Cleanup(func() { hostMountInfo, sameHostDir = oldInfo, oldSame })
}

// mountLine is one /proc/self/mountinfo line, root and point escaped as the
// kernel escapes them.
func mountLine(id, parent int, dev, root, point string) string {
	esc := strings.NewReplacer(" ", `\040`, "\t", `\011`, "\n", `\012`, `\`, `\134`)
	return strings.Join([]string{strconv.Itoa(id), strconv.Itoa(parent), dev, esc.Replace(root), esc.Replace(point),
		"rw,relatime", "shared:1", "-", "ext4", "/dev/sda1", "rw"}, " ") + "\n"
}

// A host folder that is a bind mount of a folder in the workspace is that
// folder: a hooks path through it is guarded like one spelled under the
// workspace (#308).
func TestInPlaceHooksPathThroughBindMount(t *testing.T) {
	cases := []struct {
		name   string
		src    string // the workspace folder mounted at <tmp>/mnt/tools ("<ws>/..." form)
		hp     string
		refuse string
		mounts []string // besides .git and .claude, when accepted
	}{
		{name: "hooks in a bound workspace folder are guarded",
			src: "<ws>/tools", hp: "<mnt>/hooks", mounts: []string{"tools"}},
		{name: "the bound folder itself as the hooks folder",
			src: "<ws>/tools", hp: "<mnt>", mounts: []string{"tools"}},
		{name: "a nested bound folder binds its top folder",
			src: "<ws>/tools/hooks", hp: "<mnt>", mounts: []string{"tools"}},
		{name: "a box link in the bound folder",
			src: "<ws>/tools", hp: "<mnt>/out/hooks", refuse: "reached through the symlink"},
		{name: "a missing folder in the bound folder",
			src: "<ws>/tools", hp: "<mnt>/missing", refuse: "does not exist"},
		{name: ".. below the bound folder",
			src: "<ws>/tools", hp: "<mnt>/hooks/../hooks", refuse: ".."},
		{name: ".. right after the bound folder is its host parent",
			src: "<ws>/tools", hp: "<mnt>/../hosthooks"},
		{name: "a bound folder under .git is already read-only",
			src: "<ws>/.git/hooks", hp: "<mnt>"},
		{name: "a bound folder outside the workspace is host territory",
			src: "<tmp>/elsewhere", hp: "<mnt>/hooks"},
		{name: "reached through a host link to the bound folder",
			src: "<ws>/tools", hp: "<tmp>/lnk/hooks", mounts: []string{"tools"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := realTemp(t)
			ws := filepath.Join(tmp, "Repo")
			mnt := filepath.Join(tmp, "mnt", "tools")
			r := strings.NewReplacer("<ws>", ws, "<tmp>", tmp, "<mnt>", mnt)
			src := r.Replace(tc.src)
			mkdirs(t, filepath.Join(ws, "tools", "hooks"), filepath.Join(mnt, "hooks"), filepath.Join(tmp, "mnt", "hosthooks"),
				filepath.Join(tmp, "elsewhere", "hooks"), filepath.Join(tmp, "out", "hooks"))
			symlink(t, filepath.Join(tmp, "out"), filepath.Join(ws, "tools", "out"))
			symlink(t, mnt, filepath.Join(tmp, "lnk"))
			gitRepo(t, ws, "[core]\n\thooksPath = "+r.Replace(tc.hp)+"\n")
			withMountInfo(t, mountLine(1, 0, "8:1", "/", "/")+mountLine(2, 1, "8:1", src, mnt), nil,
				func(a, b string) bool {
					rel, err := filepath.Rel(mnt, a)
					return err == nil && !strings.HasPrefix(rel, "..") && b == filepath.Join(src, rel)
				})
			got, err := inPlaceMounts(ws)
			if tc.refuse != "" {
				if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("want a refusal mentioning %q, got %v (mounts %v)", tc.refuse, err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantMounts(t, ws, got, append([]string{".git", ".claude"}, tc.mounts...)...)
		})
	}
}

// git directories and config files read outside the workspace are refused when
// a bind mount leads them back into it: a folder's, or a single file's.
func TestInPlaceGitfileThroughBindMount(t *testing.T) {
	t.Run("gitdir is a bound workspace folder", func(t *testing.T) {
		tmp := realTemp(t)
		ws := filepath.Join(tmp, "Repo")
		mnt := filepath.Join(tmp, "mnt", "gd")
		mkdirs(t, filepath.Join(ws, "gd"), mnt)
		writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+mnt+"\n")
		withMountInfo(t, mountLine(1, 0, "8:1", "/", "/")+mountLine(2, 1, "8:1", filepath.Join(ws, "gd"), mnt), nil,
			func(a, b string) bool { return a == mnt && b == filepath.Join(ws, "gd") })
		_, err := inPlaceMounts(ws)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "leads into the workspace") {
			t.Fatalf("want a refusal for a gitdir through a bind mount of a workspace folder, got %v", err)
		}
	})
	t.Run("the git config is a bound workspace file", func(t *testing.T) {
		tmp := realTemp(t)
		ws := filepath.Join(tmp, "Repo")
		gd := filepath.Join(tmp, "gd")
		cfg := filepath.Join(gd, "config")
		mkdirs(t, ws, gd)
		writeFile(t, cfg, "[core]\n")
		writeFile(t, filepath.Join(ws, "cfg"), "[core]\n")
		writeFile(t, filepath.Join(ws, ".git"), "gitdir: "+gd+"\n")
		withMountInfo(t, mountLine(1, 0, "8:1", "/", "/")+mountLine(2, 1, "8:1", filepath.Join(ws, "cfg"), cfg), nil,
			func(a, b string) bool { return a == cfg && b == filepath.Join(ws, "cfg") })
		_, err := inPlaceMounts(ws)
		if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "leads into the workspace") {
			t.Fatalf("want a refusal for a git config bound from a workspace file, got %v", err)
		}
	})
}

// Where the mount table cannot be read or does not add up, the guard cannot
// tell whether a folder is in the workspace, so it refuses (fail closed). Where
// there is no mount table to read (macOS), the walk is the one before #308.
func TestInPlaceBindMountTableFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		table  string // <ws>, <tmp>, <mnt> replaced
		err    error
		same   bool // the seam says <mnt> is <ws>/tools on disk
		refuse string
		mounts []string
	}{
		{name: "unreadable table", err: errors.New("no /proc"), refuse: "mount table"},
		{name: "no mount holds the path", table: "1 0 8:1 /x /x rw - ext4 /dev/sda1 rw\n", refuse: "mount table"},
		{name: "a malformed line", table: "1 0 8:1 / /\n", refuse: "mount table"},
		{name: "the table maps it in, the disk disagrees",
			table:  "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n2 1 8:1 <ws>/tools <mnt> rw - ext4 /dev/sda1 rw\n",
			refuse: "not the same folder"},
		{name: "another device is not the workspace",
			table: "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n2 1 8:2 <ws>/tools <mnt> rw - ext4 /dev/sdb1 rw\n"},
		{name: "a mount stacked on the bind mount hides it",
			table: "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n2 1 8:1 <ws>/tools <mnt> rw - ext4 /dev/sda1 rw\n" +
				"3 2 0:50 / <mnt> rw - tmpfs tmpfs rw\n"},
		{name: "the bind mount stacked on another one", same: true,
			table: "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n3 1 0:50 / <mnt> rw - tmpfs tmpfs rw\n" +
				"2 3 8:1 <ws>/tools <mnt> rw - ext4 /dev/sda1 rw\n",
			mounts: []string{"tools"}},
		{name: "a mount point with a space", same: true,
			table:  "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n2 1 8:1 <ws>/tools <mnt> rw - ext4 /dev/sda1 rw\n",
			mounts: []string{"tools"}},
		{name: "no mount table (macOS) keeps the walk as it was", same: true},
		// A mount at <mnt> on a first <tmp>/mnt, then a second <tmp>/mnt over that
		// one, then the bind mount at <mnt> on the second: the first <mnt> mount
		// has the same mount point but is hidden (#308 review M1).
		{name: "a mount hidden by an overmount of its parent is not the one there", same: true,
			table: "1 0 8:1 / / rw - ext4 x rw\n25 1 0:25 / <tmp>/mnt rw - tmpfs t rw\n" +
				"30 25 0:30 / <mnt> rw - tmpfs t rw\n26 25 0:26 / <tmp>/mnt rw - tmpfs t rw\n" +
				"40 26 8:1 <ws>/tools <mnt> rw - ext4 x rw\n",
			mounts: []string{"tools"}},
		// The workspace's own mount point holds a mount hidden by a later mount on
		// <tmp>: the workspace lives on that later mount.
		{name: "a hidden mount at the workspace is not its mount", same: true,
			table: "1 0 8:1 / / rw - ext4 x rw\n50 1 8:9 /old <ws> rw - ext4 y rw\n" +
				"51 1 8:1 <tmp> <tmp> rw - ext4 x rw\n40 51 8:1 <ws>/tools <mnt> rw - ext4 x rw\n",
			mounts: []string{"tools"}},
		{name: "a loop at one mount point (a repeated mount id)", refuse: "cannot read or follow its mount table",
			table: "1 0 8:1 / / rw - ext4 x rw\n5 1 8:1 / <tmp> rw - ext4 x rw\n" +
				"5 5 8:1 / <tmp> rw - ext4 x rw\n5 5 8:1 / <tmp> rw - ext4 x rw\n"},
		{name: "no root mount (a loop at /)", refuse: "mount table",
			table: "1 2 8:1 / / rw - ext4 x rw\n2 1 8:1 / / rw - ext4 x rw\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := realTemp(t)
			ws := filepath.Join(tmp, "Repo")
			mnt := filepath.Join(tmp, "mnt", "my tools")
			// The table spells paths as the kernel does: "my tools" is my\040tools.
			esc := strings.NewReplacer(" ", `\040`)
			r := strings.NewReplacer("<ws>", esc.Replace(ws), "<tmp>", esc.Replace(tmp), "<mnt>", esc.Replace(mnt))
			mkdirs(t, filepath.Join(ws, "tools", "hooks"), filepath.Join(mnt, "hooks"))
			gitRepo(t, ws, "[core]\n\thooksPath = "+mnt+"/hooks\n")
			var same func(a, b string) bool
			if tc.same {
				same = func(a, b string) bool { return a == mnt && b == filepath.Join(ws, "tools") }
			}
			withMountInfo(t, r.Replace(tc.table), tc.err, same)
			got, err := inPlaceMounts(ws)
			if tc.refuse != "" {
				if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), tc.refuse) {
					t.Fatalf("want a refusal mentioning %q, got %v (mounts %v)", tc.refuse, err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantMounts(t, ws, got, append([]string{".git", ".claude"}, tc.mounts...)...)
		})
	}
}

// A mount table that cannot be read refuses even a plain repository with no
// core.hooksPath, and the refusal names the table, not the hooks path (#308
// review M2).
func TestInPlaceMountTableRefusalNamesTheTable(t *testing.T) {
	ws := filepath.Join(realTemp(t), "Repo")
	gitRepo(t, ws, "[core]\n\tbare = false\n")
	withMountInfo(t, "", errors.New("permission denied"), nil)
	_, err := inPlaceMounts(ws)
	if !errors.Is(err, ErrUnboxableWorkspace) || !strings.Contains(err.Error(), "/proc/self/mountinfo") ||
		!strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "hooksPath") {
		t.Fatalf("want a refusal that names the mount table and not core.hooksPath, got %v", err)
	}
}

// A mount below the workspace (another filesystem mounted at <ws>/data) is in
// the workspace too: a host bind mount of a folder in it maps back through it.
func TestInPlaceHooksPathThroughBindMountOfASubmount(t *testing.T) {
	tmp := realTemp(t)
	ws := filepath.Join(tmp, "Repo")
	mnt := filepath.Join(tmp, "mnt", "x")
	mkdirs(t, filepath.Join(ws, "data", "sub", "hooks"), filepath.Join(mnt, "hooks"))
	gitRepo(t, ws, "[core]\n\thooksPath = "+mnt+"/hooks\n")
	withMountInfo(t, mountLine(1, 0, "8:1", "/", "/")+mountLine(2, 1, "8:2", "/vol", filepath.Join(ws, "data"))+
		mountLine(3, 1, "8:2", "/vol/sub", mnt), nil,
		func(a, b string) bool { return a == mnt && b == filepath.Join(ws, "data", "sub") })
	got, err := inPlaceMounts(ws)
	if err != nil {
		t.Fatal(err)
	}
	wantMounts(t, ws, got, ".git", ".claude", "data")
}

// The real mount table on this host: when some folder is mounted at two places
// (a container's bind mounts often are), the guard maps one to the other with
// the real device and inode numbers. Skipped where there is no such pair.
func TestInPlaceRealMountTableMapsABindMount(t *testing.T) {
	if hostMountInfo == nil {
		t.Skip("no mount table on this OS")
	}
	b, err := hostMountInfo()
	if err != nil {
		t.Skip(err)
	}
	ms, err := parseMountInfo(b)
	if err != nil {
		t.Fatalf("the real mount table does not parse: %v", err)
	}
	for _, outer := range ms {
		if outer.root == "/" || !outer.visibleIn(ms) {
			continue
		}
		fiO, err := os.Stat(outer.point)
		if err != nil || !fiO.IsDir() {
			continue
		}
		for _, inner := range ms {
			rel, ok := relUnder(inner.root, outer.root)
			if inner.dev != outer.dev || inner.point == outer.point || !ok || rel == "." || !inner.visibleIn(ms) ||
				strings.HasPrefix(inner.point, outer.point+"/") || strings.HasPrefix(outer.point, inner.point+"/") {
				continue
			}
			fiI, err := os.Stat(inner.point)
			if err != nil || !fiI.IsDir() {
				continue
			}
			want := filepath.Join(outer.point, rel)
			if !sameHostDir(inner.point, want) {
				continue // overmounted or gone: not a pair this test can use
			}
			got, err := wsAlias(ms, inner.point, outer.point)
			if err != nil || got != rel {
				t.Fatalf("wsAlias(%s, ws %s) = %q, %v; want %q (both are %s on %s)", inner.point, outer.point, got, err, rel, inner.root, inner.dev)
			}
			return
		}
	}
	t.Skip("no folder is mounted at two places here")
}

func TestParseMountInfo(t *testing.T) {
	in := "36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue\n" +
		`37 35 98:0 /a\040b /c\011d\134e rw - ext3 /dev/root rw` + "\n" +
		"38 35 0:4 net:[4026531840] /run/netns/x rw - nsfs nsfs rw\n"
	ms, err := parseMountInfo([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	for _, m := range ms {
		got.WriteString(strconv.Itoa(m.id) + " " + strconv.Itoa(m.parent) + " " + m.dev + " " + m.root + "|" + m.point + "\n")
	}
	want := "36 35 98:0 /mnt1|/mnt2\n37 35 98:0 /a b|/c\td\\e\n38 35 0:4 net:[4026531840]|/run/netns/x\n"
	if got.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", got.String(), want)
	}
	if _, err := parseMountInfo([]byte("1 0 8:1 / /\n")); err == nil {
		t.Fatal("a line without the separator must not parse")
	}
}
