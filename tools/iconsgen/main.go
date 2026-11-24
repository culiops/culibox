// Command iconsgen turns the pinned lucide-static and simple-icons SVG files
// into internal/web/static/icons.js, the one generated glyph table. Run it from
// the repository root (docs/ICONS.md "Regenerating"):
//
//	go run ./tools/iconsgen -names tools/iconsgen/names.txt \
//	  -lucide <dir> -lucide-version 1.70.0 -lucide-integrity sha512-... \
//	  -si <dir> -si-version 16.37.0 -si-integrity sha512-... \
//	  -o internal/web/static/icons.js
//
// -lucide and -si are the unpacked npm tarballs' icons/ directories; the
// integrity values are `npm view <pkg>@<version> dist.integrity`. A dev tool,
// not shipped in tarballs.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	var o options
	names := flag.String("names", "tools/iconsgen/names.txt", "file listing the icon names, one per line, sorted")
	out := flag.String("o", "internal/web/static/icons.js", "output file")
	flag.StringVar(&o.LucideDir, "lucide", "", "directory of lucide-static SVG files")
	flag.StringVar(&o.LucideVersion, "lucide-version", "", "lucide-static version the files must come from")
	flag.StringVar(&o.LucideIntegrity, "lucide-integrity", "", "lucide-static tarball integrity (sha512-...)")
	flag.StringVar(&o.SIDir, "si", "", "directory of simple-icons SVG files")
	flag.StringVar(&o.SIVersion, "si-version", "", "simple-icons version")
	flag.StringVar(&o.SIIntegrity, "si-integrity", "", "simple-icons tarball integrity (sha512-...)")
	flag.Parse()

	if err := run(o, *names, *out); err != nil {
		fmt.Fprintln(os.Stderr, "iconsgen:", err)
		os.Exit(1)
	}
}

func run(o options, namesFile, out string) error {
	f, err := os.Open(namesFile)
	if err != nil {
		return err
	}
	defer f.Close()
	if o.Names, err = readNames(f); err != nil {
		return err
	}
	js, err := generate(o)
	if err != nil {
		return err
	}
	return os.WriteFile(out, js, 0o644)
}
