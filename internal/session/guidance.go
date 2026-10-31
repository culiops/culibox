package session

// cloneGuidance is the one line a boxed claude in a private copy is told at
// start (claude --append-system-prompt): where its work goes. push says the
// binding can push to the clone's origin; autoReturn says the branches come
// home on their own when the session ends.
func cloneGuidance(of string, push, autoReturn bool) string {
	s := "CuliBox: you are working in a private copy of " + of + ". "
	if push {
		s += `Commit on a branch, push it with "git push -u origin <branch>" and open a PR with "gh pr create"; pushing to main may be rejected by branch protection. `
		if autoReturn {
			return s + "Your branches also come home to " + of + " as box/<branch> when this session ends."
		}
		return s + "Your branches reach " + of + " as box/<branch> when the user brings them home."
	}
	s += "It cannot push to GitHub from here: commit on a branch, and your branches "
	if autoReturn {
		return s + "come home to " + of + " as box/<branch> when this session ends."
	}
	return s + "reach " + of + " as box/<branch> when the user brings them home."
}
