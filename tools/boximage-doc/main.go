// Command boximage-doc writes docs/BOX-IMAGE.md, the inventory of the box image,
// from docker/culibox/Dockerfile. Run it from the repository root:
//
//	go run ./tools/boximage-doc -w
//
// Without -w it prints the doc. A test fails when the committed doc drifts from
// the Dockerfile. A docs helper, not shipped in tarballs.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	write := flag.Bool("w", false, "write docs/BOX-IMAGE.md instead of printing it")
	flag.Parse()
	df, err := os.ReadFile("docker/culibox/Dockerfile")
	if err != nil {
		fmt.Fprintln(os.Stderr, "boximage-doc: run from the repository root:", err)
		os.Exit(1)
	}
	doc, err := Render(df)
	if err != nil {
		fmt.Fprintln(os.Stderr, "boximage-doc:", err)
		os.Exit(1)
	}
	if !*write {
		fmt.Print(doc)
		return
	}
	if err := os.WriteFile("docs/BOX-IMAGE.md", []byte(doc), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "boximage-doc:", err)
		os.Exit(1)
	}
}
