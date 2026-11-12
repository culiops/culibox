// Command release-status renders and rewrites a release's platform status block and
// builds macos-verification.json. It is a build/release helper, not shipped in tarballs.
//
//	notes      --tag T --ci-run URL --releases releases.json
//	check-sums --sums SHA256SUMS <file>...
//	record     --tag T --commit C --in existing.json|"" --run run.json
//	rewrite    --notes notes.md (--record record.json | --not-verified)
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "release-status:", err)
		os.Exit(1)
	}
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// filesByBase keys paths by base name, which is how SHA256SUMS lists them. Two paths
// with the same base name would collapse to one and leave a file unchecked, so that is
// an error.
func filesByBase(paths []string) (map[string]string, error) {
	files := map[string]string{}
	for _, p := range paths {
		name := filepath.Base(p)
		if prev, dup := files[name]; dup {
			return nil, fmt.Errorf("%s and %s have the same file name %s; check them one at a time", prev, p, name)
		}
		files[name] = p
	}
	return files, nil
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: release-status notes|check-sums|record|rewrite [flags]")
	}
	cmd, args := args[0], args[1:]
	fs := newFlags(cmd)
	switch cmd {
	case "notes":
		tag := fs.String("tag", "", "the release tag")
		ci := fs.String("ci-run", "", "URL of the CI run that tested this release")
		rel := fs.String("releases", "", "GitHub Releases API response (JSON array)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *tag == "" || *ci == "" || *rel == "" {
			return errors.New("notes needs --tag, --ci-run and --releases")
		}
		var rs []Release
		if err := readJSON(*rel, &rs); err != nil {
			return err
		}
		last, ok := LastVerified(rs, *tag)
		_, err := fmt.Fprintln(out, RenderBlock(*tag, *ci, last, ok, ""))
		return err

	case "check-sums":
		sums := fs.String("sums", "", "SHA256SUMS file")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *sums == "" || fs.NArg() == 0 {
			return errors.New("check-sums needs --sums and at least one file")
		}
		body, err := os.ReadFile(*sums)
		if err != nil {
			return err
		}
		files, err := filesByBase(fs.Args())
		if err != nil {
			return err
		}
		if err := CheckSums(body, files); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "ok: %d file(s) match SHA256SUMS\n", len(files))
		return err

	case "record":
		tag := fs.String("tag", "", "the release tag")
		commit := fs.String("commit", "", "the commit the tag points at")
		in := fs.String("in", "", "existing macos-verification.json, or empty for none")
		runPath := fs.String("run", "", "run.json for the new run")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *runPath == "" {
			return errors.New("record needs --run")
		}
		var r Run
		if err := readJSON(*runPath, &r); err != nil {
			return err
		}
		var existing *Record
		if *in != "" {
			existing = &Record{}
			if err := readJSON(*in, existing); err != nil {
				return err
			}
		}
		rec, err := AddRun(existing, *tag, *commit, r)
		if err != nil {
			return err
		}
		b, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%s\n", b)
		return err

	case "rewrite":
		notes := fs.String("notes", "", "release notes file holding the status block")
		recPath := fs.String("record", "", "macos-verification.json")
		reset := fs.Bool("not-verified", false, "put both darwin lines back to NOT yet verified (instead of --record)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *notes == "" || (*recPath == "") == !*reset {
			return errors.New("rewrite needs --notes and exactly one of --record or --not-verified")
		}
		body, err := os.ReadFile(*notes)
		if err != nil {
			return err
		}
		if *reset {
			res, err := RewriteDarwin(string(body), notYetVerified)
			if err != nil {
				return err
			}
			_, err = io.WriteString(out, res)
			return err
		}
		var rec Record
		if err := readJSON(*recPath, &rec); err != nil {
			return err
		}
		if len(rec.Runs) == 0 {
			return errors.New("the record has no runs")
		}
		res, err := RewriteDarwin(string(body), DarwinLine(rec.Runs[len(rec.Runs)-1]))
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, res)
		return err
	}
	return fmt.Errorf("unknown subcommand %q (want notes, check-sums, record or rewrite)", cmd)
}
