package handback

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"io/fs"
	"path"
	"slices"
	"strings"
	"unicode"
)

// debug/macho has no consts for these: the 64-bit-offset fat magic and the
// byte-swapped thin magics (what a little-endian Mach-O file starts with when
// the first four bytes are read big-endian).
const (
	fatMagic64      = 0xcafebabf
	magic32Swapped  = 0xcefaedfe
	magic64Swapped  = 0xcffaedfe
	maxMachoFileTyp = 12 // MH_FILESET, the last filetype in <mach-o/loader.h>
)

// mismatchExt is the fixed set of extensions that claim a non-runnable type.
// Content that is runnable or an archive under one of these is disguised.
var mismatchExt = map[string]bool{
	"pdf": true, "png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true,
	"bmp": true, "svg": true, "txt": true, "md": true, "csv": true, "json": true,
	"yaml": true, "doc": true, "docx": true, "xls": true, "xlsx": true, "ppt": true,
	"pptx": true, "odt": true, "rtf": true, "mp3": true, "mp4": true, "mov": true,
	"wav": true,
}

// zipByDesign are formats that are zips on purpose: a zip under one of these
// extensions is neither a mismatch nor worth flagging as an archive.
var zipByDesign = map[string]bool{
	"docx": true, "xlsx": true, "pptx": true, "odt": true, "jar": true, "apk": true, "epub": true,
}

// Classify decides which classes a file belongs to from its base name, its
// mode (exec and setuid/setgid bits) and the first HeadBytes of its content.
// It is pure: no I/O, and it never parses past the head. detail is a short
// sentence for the panel, built only from fixed words and the extension table,
// so it carries nothing the box wrote.
func Classify(name string, mode fs.FileMode, head []byte) (classes []Class, detail string) {
	// Windows ignores trailing spaces and dots ("report.pdf " opens as
	// report.pdf), so a disguise must not escape the table with them.
	name = strings.TrimRightFunc(name, func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))

	var content Class
	var what string
	exe := executableKind(head)
	switch {
	case exe != "":
		content, what = ClassExecutable, exe
	case bytes.HasPrefix(head, []byte("#!")) && mode&0o111 != 0:
		// A shebang file nobody can run is just text; the exec bit is the signal.
		content, what = ClassScript, "script with exec bit"
	default:
		if kind := archiveKind(head); kind != "" {
			if kind == "zip" && zipByDesign[ext] {
				break
			}
			content, what = ClassArchive, kind+" archive"
		}
	}

	if content != "" {
		if mismatchExt[ext] {
			classes = append(classes, ClassMismatch)
			what += ", extension says " + strings.ToUpper(ext)
		}
		classes = append(classes, content)
		detail = what
	}

	if mode&(fs.ModeSetuid|fs.ModeSetgid) != 0 {
		classes = append(classes, ClassSetID)
		bits := "setuid bit"
		switch {
		case mode&fs.ModeSetuid != 0 && mode&fs.ModeSetgid != 0:
			bits = "setuid and setgid bits"
		case mode&fs.ModeSetgid != 0:
			bits = "setgid bit"
		}
		if detail != "" {
			detail += "; " + bits
		} else {
			detail = bits
		}
	}
	return classes, detail
}

// executableKind names the ELF, Mach-O or PE kind in head, or "" if it is none.
// Magic and header fields only; the debug/* NewFile readers seek past the head.
func executableKind(head []byte) string {
	if bytes.HasPrefix(head, []byte(elf.ELFMAG)) {
		return elfKind(head)
	}
	if k := machoKind(head); k != "" {
		return k
	}
	return peKind(head)
}

func elfKind(head []byte) string {
	if len(head) < 18 { // e_type ends at offset 18
		return ""
	}
	var order binary.ByteOrder
	switch elf.Data(head[elf.EI_DATA]) {
	case elf.ELFDATA2LSB:
		order = binary.LittleEndian
	case elf.ELFDATA2MSB:
		order = binary.BigEndian
	default:
		return ""
	}
	switch elf.Type(order.Uint16(head[16:18])) {
	case elf.ET_EXEC:
		return "ELF executable"
	case elf.ET_DYN:
		return "ELF shared object"
	case elf.ET_REL:
		return "ELF relocatable object"
	}
	return ""
}

func machoKind(head []byte) string {
	if len(head) < 4 {
		return ""
	}
	magic := binary.BigEndian.Uint32(head)
	switch magic {
	case macho.MagicFat, fatMagic64:
		// 0xcafebabe is also the Java class magic. There the next u32 is
		// minor<<16|major with major >= 45, so a small arch count (the real
		// range is 1..29) tells the two apart.
		if len(head) < 8 {
			return ""
		}
		if n := binary.BigEndian.Uint32(head[4:8]); n >= 1 && n <= 29 {
			return "Mach-O universal binary"
		}
		return ""
	}
	var order binary.ByteOrder
	switch magic {
	case macho.Magic32, macho.Magic64: // bytes as written: a big-endian file
		order = binary.BigEndian
	case magic32Swapped, magic64Swapped: // byte-swapped: a little-endian file
		order = binary.LittleEndian
	default:
		return ""
	}
	if len(head) < 16 { // magic, cputype, cpusubtype, filetype
		return ""
	}
	ft := order.Uint32(head[12:16])
	if ft < 1 || ft > maxMachoFileTyp { // not a real filetype: random bytes after a magic
		return ""
	}
	switch macho.Type(ft) {
	case macho.TypeExec:
		return "Mach-O executable"
	case macho.TypeDylib:
		return "Mach-O dynamic library"
	case macho.TypeBundle:
		return "Mach-O bundle"
	case macho.TypeObj:
		return "Mach-O object"
	}
	return "Mach-O file"
}

func peKind(head []byte) string {
	if len(head) < 0x40 || head[0] != 'M' || head[1] != 'Z' {
		return ""
	}
	off := int64(binary.LittleEndian.Uint32(head[0x3c:0x40])) // e_lfanew
	if off+4 > int64(len(head)) || !bytes.Equal(head[off:off+4], []byte("PE\x00\x00")) {
		return ""
	}
	return "PE executable"
}

// archiveKind names the archive format in head, or "" if it is none.
func archiveKind(head []byte) string {
	for _, m := range []struct {
		magic, kind string
	}{
		{"PK\x03\x04", "zip"},
		{"\x1f\x8b", "gzip"},
		{"\xfd7zXZ\x00", "xz"},
		{"\x28\xb5\x2f\xfd", "zstd"},
		{"7z\xbc\xaf\x27\x1c", "7z"},
		{"Rar!\x1a\x07", "rar"},
	} {
		if bytes.HasPrefix(head, []byte(m.magic)) {
			return m.kind
		}
	}
	// "BZh" then the block-size digit; the bare prefix matches ordinary text.
	if len(head) >= 4 && string(head[:3]) == "BZh" && head[3] >= '1' && head[3] <= '9' {
		return "bzip2"
	}
	if len(head) >= 262 && bytes.Equal(head[257:262], []byte("ustar")) {
		return "tar"
	}
	return ""
}

// SymlinkThroughNote is the detail of a symlink flagged by ClimbsThroughName.
const SymlinkThroughNote = "symlink climbs back out of another path in its target; where it leads cannot be told from its text"

// OutsideRoot reports whether a symlink may leave the workspace, judged on the
// link text alone (nothing is resolved or followed). linkPath is the link's
// slash-separated path relative to the root; the target may leave when
// LeavesByText or ClimbsThroughName.
func OutsideRoot(linkPath, target string) bool {
	return LeavesByText(linkPath, target) || ClimbsThroughName(target)
}

// LeavesByText reports whether target is absolute or its ".." steps, read as
// text, climb past the root. A link OutsideRoot flags for any other reason
// gets SymlinkThroughNote as its detail.
func LeavesByText(linkPath, target string) bool {
	if strings.HasPrefix(target, "/") {
		return true
	}
	joined := path.Join(path.Dir(linkPath), target)
	return joined == ".." || strings.HasPrefix(joined, "../")
}

// ClimbsThroughName reports whether a relative target has a ".." step after a
// named step ("s/.." in "s/../../x"). The kernel resolves ".." from where the
// named step leads, not from its text: when s is itself a symlink in the tree
// (now or after a later change), "s/.." can be anywhere, outside the root
// included, while the text looks inside. The text alone cannot tell, so such a
// target is always treated as leaving. ".." steps at the start climb through
// the link's own parent directories, which a walk (or a git tree) has seen as
// directories, so they are judged on the text.
func ClimbsThroughName(target string) bool {
	named := false
	for _, step := range strings.Split(target, "/") {
		switch step {
		case "", ".":
		case "..":
			if named {
				return true
			}
		default:
			named = true
		}
	}
	return false
}

// rank orders classes for SortFlags: the lower, the sooner shown.
func rank(c Class) int {
	switch c {
	case ClassMismatch:
		return 0
	case ClassSetID:
		return 1
	case ClassSymlinkOut:
		return 2
	case ClassExecutable:
		return 3
	case ClassScript:
		return 4
	case ClassArchive:
		return 5
	}
	return 6
}

func bestRank(f Flag) int {
	best := 6
	for _, c := range f.Classes {
		best = min(best, rank(c))
	}
	return best
}

// SortFlags orders flags for display: extension mismatches first, then
// set-id, outward symlinks, executables, scripts and archives (by each flag's
// strongest class), then by path.
func SortFlags(flags []Flag) {
	slices.SortStableFunc(flags, func(a, b Flag) int {
		if ra, rb := bestRank(a), bestRank(b); ra != rb {
			return ra - rb
		}
		return strings.Compare(a.Path, b.Path)
	})
}
