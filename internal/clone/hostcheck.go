package clone

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// Host-side queries about clone_of's own history (spec: clone refresh). They
// run git in the user's repo only, with CleanEnv and hardened(), never against
// a clone's .git. Every SHA and branch name is validated before it reaches git.

// BaseBranch resolves the branch a clone follows: local main, else master. It
// returns ("", "", nil) when neither exists.
func BaseBranch(ctx context.Context, g Git, of string) (branch, sha string, err error) {
	for _, b := range []string{"main", "master"} {
		out, err := g.Run(ctx, of, CleanEnv(), hardened("rev-parse", "--verify", "--quiet", "--end-of-options", "refs/heads/"+b+"^{commit}")...)
		if err != nil {
			if code, ok := exitCode(err); ok && code == 1 { // --quiet: no such ref
				continue
			}
			return "", "", err
		}
		sha = strings.TrimSpace(string(out))
		if !shaRE.MatchString(sha) {
			return "", "", fmt.Errorf("rev-parse refs/heads/%s: unexpected output %q", b, sanitize(sha))
		}
		return b, sha, nil
	}
	return "", "", nil
}

// StaleSHAs returns the recorded SHAs that are no longer part of refs/heads/<base>
// history: gone from the repo, or not reachable from base. It is two git calls
// however many SHAs there are, and keeps the record's order.
func StaleSHAs(ctx context.Context, g Git, of, base string, recorded []string) ([]string, error) {
	sg, ok := g.(StdinGit)
	if !ok {
		return nil, fmt.Errorf("StaleSHAs needs a Git that can feed stdin")
	}
	if err := ValidBranch(base); err != nil {
		return nil, err
	}
	var uniq []string
	seen := map[string]bool{}
	for _, s := range recorded {
		if !shaRE.MatchString(s) {
			return nil, fmt.Errorf("recorded base %q is not a commit SHA", sanitize(s))
		}
		if !seen[s] {
			seen[s] = true
			uniq = append(uniq, s)
		}
	}
	if len(uniq) == 0 {
		return nil, nil
	}
	list := strings.Join(uniq, "\n") + "\n"
	out, err := sg.RunStdin(ctx, of, CleanEnv(), strings.NewReader(list), hardened("cat-file", "--batch-check")...)
	if err != nil {
		return nil, err
	}
	// One line per input, in order: "<sha> <type> <size>" or "<sha> missing".
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != len(uniq) {
		return nil, fmt.Errorf("cat-file --batch-check: %d answers for %d SHAs", len(lines), len(uniq))
	}
	stale := map[string]bool{}
	var exist []string
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != uniq[i] {
			return nil, fmt.Errorf("cat-file --batch-check: unexpected line %q", sanitize(line))
		}
		if f[1] == "commit" {
			exist = append(exist, uniq[i])
		} else {
			stale[uniq[i]] = true // missing, or not a commit
		}
	}
	if len(exist) > 0 {
		in := "^refs/heads/" + base + "\n" + strings.Join(exist, "\n") + "\n"
		out, err := sg.RunStdin(ctx, of, CleanEnv(), strings.NewReader(in), hardened("rev-list", "--stdin")...)
		if err != nil {
			return nil, err
		}
		for _, l := range bytes.Fields(out) {
			if s := string(l); seen[s] {
				stale[s] = true
			}
		}
	}
	var res []string
	for _, s := range uniq {
		if stale[s] {
			res = append(res, s)
		}
	}
	return res, nil
}

// HeadInHost reports whether the commit sha is in clone_of and reachable from
// some local branch. nil means unknown (git could not be run, the context
// ended, or sha is malformed).
func HeadInHost(ctx context.Context, g Git, of, sha string) *bool {
	if !shaRE.MatchString(sha) {
		return nil
	}
	verdict := func(b bool) *bool { return &b }
	if _, err := g.Run(ctx, of, CleanEnv(), hardened("cat-file", "-e", sha+"^{commit}")...); err != nil {
		if code, ok := exitCode(err); ok && code > 0 && ctx.Err() == nil {
			return verdict(false)
		}
		return nil
	}
	out, err := g.Run(ctx, of, CleanEnv(), hardened("for-each-ref", "--count=1", "--contains", sha, "refs/heads/")...)
	if err != nil || ctx.Err() != nil {
		return nil
	}
	return verdict(len(bytes.TrimSpace(out)) > 0)
}
