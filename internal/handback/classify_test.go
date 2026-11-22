package handback

import (
	"encoding/binary"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

// pad extends b with zeros to n bytes, like the rest of a real file head.
func pad(b []byte, n int) []byte {
	if len(b) >= n {
		return b
	}
	return append(b, make([]byte, n-len(b))...)
}

func elfHead(order binary.ByteOrder, etype uint16) []byte {
	h := make([]byte, 64)
	copy(h, "\x7fELF")
	h[4] = 2 // 64-bit
	if order == binary.LittleEndian {
		h[5] = 1
	} else {
		h[5] = 2
	}
	h[6] = 1
	order.PutUint16(h[16:], etype)
	return h
}

func machoHead(magicLE bool, magic, ftype uint32) []byte {
	h := make([]byte, 32)
	if magicLE {
		binary.LittleEndian.PutUint32(h, magic)
		binary.LittleEndian.PutUint32(h[12:], ftype)
	} else {
		binary.BigEndian.PutUint32(h, magic)
		binary.BigEndian.PutUint32(h[12:], ftype)
	}
	return h
}

func fatHead(magic, second uint32) []byte {
	h := make([]byte, 64)
	binary.BigEndian.PutUint32(h, magic)
	binary.BigEndian.PutUint32(h[4:], second)
	return h
}

func peHead(lfanew uint32, withSig bool) []byte {
	h := make([]byte, 0x100)
	copy(h, "MZ")
	binary.LittleEndian.PutUint32(h[0x3c:], lfanew)
	if withSig {
		copy(h[lfanew:], "PE\x00\x00")
	}
	return h
}

func tarHead() []byte {
	h := make([]byte, 512)
	copy(h[257:], "ustar")
	return h
}

func TestClassify(t *testing.T) {
	le, be := binary.LittleEndian, binary.BigEndian
	const (
		mm = ClassMismatch
		ex = ClassExecutable
		sc = ClassScript
		ar = ClassArchive
		si = ClassSetID
	)
	tests := []struct {
		name       string
		file       string
		mode       fs.FileMode
		head       []byte
		want       []Class
		detailHas  string // substring; "" skips
		detailWant string // exact; "" skips
	}{
		{name: "ELF ET_EXEC", file: "a.out", mode: 0o755, head: elfHead(le, 2), want: []Class{ex}, detailHas: "ELF"},
		{name: "ELF ET_DYN", file: "a.out", mode: 0o755, head: elfHead(le, 3), want: []Class{ex}, detailWant: "ELF shared object"},
		{name: "ELF ET_REL", file: "a.out", mode: 0o644, head: elfHead(le, 1), want: []Class{ex}, detailHas: "ELF"},
		{name: "ELF big-endian", file: "a.out", mode: 0o644, head: elfHead(be, 2), want: []Class{ex}, detailHas: "ELF"},
		{name: "ELF ET_CORE is not flagged", file: "core", mode: 0o644, head: elfHead(le, 4), want: nil},
		{name: "ELF bad data byte", file: "a.out", mode: 0o644, head: func() []byte { h := elfHead(le, 2); h[5] = 9; return h }(), want: nil},
		{name: "ELF cut short", file: "a.out", mode: 0o644, head: []byte("\x7fELF\x02\x01"), want: nil},
		{name: "ELF as pdf", file: "report.pdf", mode: 0o644, head: elfHead(le, 2), want: []Class{mm, ex}, detailWant: "ELF executable, extension says PDF"},
		{name: "ELF as uppercase PDF", file: "REPORT.PDF", mode: 0o644, head: elfHead(le, 2), want: []Class{mm, ex}, detailWant: "ELF executable, extension says PDF"},

		{name: "Mach-O 64 LE", file: "tool", mode: 0o755, head: machoHead(true, 0xfeedfacf, 2), want: []Class{ex}, detailWant: "Mach-O executable"},
		{name: "Mach-O 64 byte-swapped", file: "tool", mode: 0o755, head: machoHead(true, 0xcffaedfe, 2), want: []Class{ex}},
		{name: "Mach-O 32 LE", file: "tool", mode: 0o755, head: machoHead(true, 0xfeedface, 2), want: []Class{ex}},
		{name: "Mach-O 64 BE", file: "tool", mode: 0o755, head: machoHead(false, 0xfeedfacf, 2), want: []Class{ex}},
		{name: "Mach-O dylib", file: "x.dylib", mode: 0o644, head: machoHead(true, 0xfeedfacf, 6), want: []Class{ex}, detailWant: "Mach-O dynamic library"},
		{name: "Mach-O bundle", file: "x.bundle", mode: 0o644, head: machoHead(true, 0xfeedfacf, 8), want: []Class{ex}, detailWant: "Mach-O bundle"},
		{name: "Mach-O object", file: "x.o", mode: 0o644, head: machoHead(true, 0xfeedfacf, 1), want: []Class{ex}, detailWant: "Mach-O object"},
		{name: "Mach-O cut short", file: "tool", mode: 0o755, head: []byte{0xcf, 0xfa, 0xed, 0xfe, 7, 0, 0, 1}, want: nil},
		{name: "Mach-O as png", file: "logo.png", mode: 0o644, head: machoHead(true, 0xfeedfacf, 2), want: []Class{mm, ex}, detailWant: "Mach-O executable, extension says PNG"},
		{name: "fat Mach-O 2 archs", file: "tool", mode: 0o755, head: fatHead(0xcafebabe, 2), want: []Class{ex}, detailWant: "Mach-O universal binary"},
		{name: "fat Mach-O 29 archs", file: "tool", mode: 0o755, head: fatHead(0xcafebabe, 29), want: []Class{ex}},
		{name: "fat Mach-O 0 archs", file: "tool", mode: 0o755, head: fatHead(0xcafebabe, 0), want: nil},
		{name: "fat Mach-O 30 archs", file: "tool", mode: 0o755, head: fatHead(0xcafebabe, 30), want: nil},
		{name: "fat Mach-O 64", file: "tool", mode: 0o755, head: fatHead(0xcafebabf, 2), want: []Class{ex}},
		{name: "Java class major 61", file: "A.class", mode: 0o644, head: fatHead(0xcafebabe, 61), want: nil},
		{name: "Java class major 45", file: "A.class", mode: 0o644, head: fatHead(0xcafebabe, 45), want: nil},
		{name: "Java class with minor", file: "A.class", mode: 0o644, head: fatHead(0xcafebabe, 3<<16|52), want: nil},
		{name: "Java class renamed pdf", file: "x.pdf", mode: 0o644, head: fatHead(0xcafebabe, 61), want: nil},

		{name: "PE", file: "setup.exe", mode: 0o644, head: peHead(0x80, true), want: []Class{ex}, detailWant: "PE executable"},
		{name: "PE as doc", file: "cv.docx", mode: 0o644, head: peHead(0x80, true), want: []Class{mm, ex}, detailWant: "PE executable, extension says DOCX"},
		{name: "MZ e_lfanew beyond head", file: "x.exe", mode: 0o644, head: peHead(0x80, false)[:0x40+0x20], want: nil},
		{name: "MZ e_lfanew huge", file: "x.exe", mode: 0o644, head: func() []byte { h := peHead(0x80, false); binary.LittleEndian.PutUint32(h[0x3c:], 0xffffffff); return h }(), want: nil},
		{name: "MZ without PE signature", file: "x.exe", mode: 0o644, head: peHead(0x80, false), want: nil},
		{name: "MZ too short", file: "x.exe", mode: 0o644, head: []byte("MZ"), want: nil},

		{name: "script with exec bit", file: "run", mode: 0o755, head: []byte("#!/bin/sh\necho hi\n"), want: []Class{sc}, detailWant: "script with exec bit"},
		{name: "script without exec bit", file: "run", mode: 0o644, head: []byte("#!/bin/sh\necho hi\n"), want: nil},
		{name: "script group exec only", file: "run", mode: 0o710, head: []byte("#!/bin/sh\n"), want: []Class{sc}},
		{name: "script named txt", file: "notes.txt", mode: 0o755, head: []byte("#!/bin/sh\n"), want: []Class{mm, sc}, detailWant: "script with exec bit, extension says TXT"},
		{name: "script named txt without exec bit", file: "notes.txt", mode: 0o644, head: []byte("#!/bin/sh\n"), want: nil},

		{name: "zip", file: "x.zip", mode: 0o644, head: pad([]byte("PK\x03\x04"), 64), want: []Class{ar}, detailWant: "zip archive"},
		{name: "zip as pdf", file: "doc.pdf", mode: 0o644, head: pad([]byte("PK\x03\x04"), 64), want: []Class{mm, ar}, detailWant: "zip archive, extension says PDF"},
		{name: "zip as docx", file: "a.docx", mode: 0o644, head: pad([]byte("PK\x03\x04"), 64), want: nil},
		{name: "zip as xlsx", file: "a.xlsx", mode: 0o644, head: pad([]byte("PK\x03\x04"), 64), want: nil},
		{name: "zip as jar", file: "a.jar", mode: 0o644, head: pad([]byte("PK\x03\x04"), 64), want: nil},
		{name: "zip as apk", file: "a.apk", mode: 0o644, head: pad([]byte("PK\x03\x04"), 64), want: nil},
		{name: "zip as epub", file: "a.epub", mode: 0o644, head: pad([]byte("PK\x03\x04"), 64), want: nil},
		{name: "gzip as docx is a mismatch", file: "a.docx", mode: 0o644, head: []byte("\x1f\x8b\x08\x00"), want: []Class{mm, ar}},
		{name: "gzip", file: "x.gz", mode: 0o644, head: []byte("\x1f\x8b\x08\x00"), want: []Class{ar}, detailWant: "gzip archive"},
		{name: "bzip2", file: "x.bz2", mode: 0o644, head: []byte("BZh91AY"), want: []Class{ar}},
		{name: "xz", file: "x.xz", mode: 0o644, head: []byte("\xfd7zXZ\x00\x00"), want: []Class{ar}},
		{name: "zstd", file: "x.zst", mode: 0o644, head: []byte("\x28\xb5\x2f\xfd\x00"), want: []Class{ar}},
		{name: "7z", file: "x.7z", mode: 0o644, head: []byte("7z\xbc\xaf\x27\x1c\x00"), want: []Class{ar}},
		{name: "rar", file: "x.rar", mode: 0o644, head: []byte("Rar!\x1a\x07\x00"), want: []Class{ar}},
		{name: "tar", file: "x.tar", mode: 0o644, head: tarHead(), want: []Class{ar}, detailWant: "tar archive"},
		{name: "tar marker too early", file: "x.txt", mode: 0o644, head: func() []byte { h := make([]byte, 512); copy(h[256:], "ustar"); return h }(), want: nil},
		{name: "tar head too short for the marker", file: "x.tar", mode: 0o644, head: make([]byte, 260), want: nil},

		{name: "setuid text file", file: "x.txt", mode: fs.ModeSetuid | 0o755, head: []byte("hello"), want: []Class{si}, detailWant: "setuid bit"},
		{name: "setgid text file", file: "x.txt", mode: fs.ModeSetgid | 0o755, head: []byte("hello"), want: []Class{si}, detailWant: "setgid bit"},
		{name: "setuid and setgid", file: "x.txt", mode: fs.ModeSetuid | fs.ModeSetgid | 0o755, head: []byte("hello"), want: []Class{si}, detailWant: "setuid and setgid bits"},
		{name: "setuid ELF", file: "su", mode: fs.ModeSetuid | 0o755, head: elfHead(le, 2), want: []Class{ex, si}, detailWant: "ELF executable; setuid bit"},
		{name: "setuid ELF as pdf", file: "x.pdf", mode: fs.ModeSetuid | 0o755, head: elfHead(le, 2), want: []Class{mm, ex, si}, detailWant: "ELF executable, extension says PDF; setuid bit"},

		{name: "plain text", file: "a.txt", mode: 0o644, head: []byte("hello\n"), want: nil},
		{name: "empty", file: "a.txt", mode: 0o644, head: nil, want: nil},
		{name: "png", file: "a.png", mode: 0o644, head: []byte("\x89PNG\r\n\x1a\n"), want: nil},
		{name: "jpeg", file: "a.jpg", mode: 0o644, head: []byte("\xff\xd8\xff\xe0"), want: nil},
		{name: "pdf", file: "a.pdf", mode: 0o644, head: []byte("%PDF-1.7\n"), want: nil},
		{name: "no extension", file: "Makefile", mode: 0o644, head: []byte("all:\n"), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, detail := Classify(tt.file, tt.mode, tt.head)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("classes = %v, want %v (detail %q)", got, tt.want, detail)
			}
			if tt.detailHas != "" && !strings.Contains(detail, tt.detailHas) {
				t.Errorf("detail = %q, want it to contain %q", detail, tt.detailHas)
			}
			if tt.detailWant != "" && detail != tt.detailWant {
				t.Errorf("detail = %q, want %q", detail, tt.detailWant)
			}
			if len(got) == 0 && detail != "" {
				t.Errorf("no classes but detail %q", detail)
			}
		})
	}
}

func TestOutsideRoot(t *testing.T) {
	for _, tt := range []struct {
		link, target string
		want         bool
	}{
		{"a/l", "/etc/passwd", true},
		{"a/l", "../../x", true},
		{"a/l", "../b", false},
		{"l", "b/c", false},
		{"l", "..", true},
		{"l", "../x", true},
		{"a/l", "..", false},
		{"a/b/l", "../../..", true},
		{"a/l", "./b/../c", false},
		{"a/l", "b/../../../x", true},
		{"l", "", false},
		{"l", "/", true},
	} {
		if got := OutsideRoot(tt.link, tt.target); got != tt.want {
			t.Errorf("OutsideRoot(%q, %q) = %v, want %v", tt.link, tt.target, got, tt.want)
		}
	}
}

func TestSortFlags(t *testing.T) {
	flags := []Flag{
		{Path: "z.zip", Classes: []Class{ClassArchive}},
		{Path: "b.sh", Classes: []Class{ClassScript}},
		{Path: "a.bin", Classes: []Class{ClassExecutable}},
		{Path: "ln", Classes: []Class{ClassSymlinkOut}},
		{Path: "su", Classes: []Class{ClassExecutable, ClassSetID}},
		{Path: "y.pdf", Classes: []Class{ClassMismatch, ClassExecutable}},
		{Path: "a.pdf", Classes: []Class{ClassMismatch, ClassArchive}},
		{Path: "a.zip", Classes: []Class{ClassArchive}},
	}
	SortFlags(flags)
	var got []string
	for _, f := range flags {
		got = append(got, f.Path)
	}
	want := []string{"a.pdf", "y.pdf", "su", "ln", "a.bin", "b.sh", "a.zip", "z.zip"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestDefaultLimits(t *testing.T) {
	l := DefaultLimits()
	if l.Entries != 500_000 || l.Depth != 64 || l.Candidates != 50_000 || l.MaxFlags != 5_000 ||
		l.Budget.Seconds() != 30 || l.HashPerFile != 64<<20 || l.HashTotal != 512<<20 {
		t.Fatalf("DefaultLimits() = %+v", l)
	}
	if HeadBytes != 8192 {
		t.Fatalf("HeadBytes = %d", HeadBytes)
	}
}
