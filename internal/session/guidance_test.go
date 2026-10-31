package session

import "testing"

func TestCloneGuidance(t *testing.T) {
	const head = "CuliBox: you are working in a private copy of /home/u/repo. "
	const pushSentences = `Commit on a branch, push it with "git push -u origin <branch>" and open a PR with "gh pr create"; pushing to main may be rejected by branch protection. `
	cases := []struct {
		name             string
		push, autoReturn bool
		want             string
	}{
		{"push, auto", true, true, head + pushSentences +
			"Your branches also come home to /home/u/repo as box/<branch> when this session ends."},
		{"push, no auto", true, false, head + pushSentences +
			"Your branches reach /home/u/repo as box/<branch> when the user brings them home."},
		{"no push, auto", false, true, head +
			"It cannot push to GitHub from here: commit on a branch, and your branches come home to /home/u/repo as box/<branch> when this session ends."},
		{"no push, no auto", false, false, head +
			"It cannot push to GitHub from here: commit on a branch, and your branches reach /home/u/repo as box/<branch> when the user brings them home."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cloneGuidance("/home/u/repo", c.push, c.autoReturn); got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}
