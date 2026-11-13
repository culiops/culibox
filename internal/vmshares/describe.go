package vmshares

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Row is a share with the text the VM folders page shows for it.
type Row struct {
	Share
	Display, Origin string
	UsedBy          []string
}

// WholeHomeLine is the doctor/serve warning for a VM that still shares all of
// $HOME; n is the number of folders the proposed list holds.
func WholeHomeLine(n int) string {
	return fmt.Sprintf("the box VM shares your whole home folder; apply the folder list in Settings › VM folders (or `culibox box share apply`) so it sees only %d folders", n)
}

// Describe explains each share: where it came from and which profiles use it.
// Running sessions are added by the service, not here.
func Describe(list []Share, profiles []Profile, recorded []string, home string, fold bool) []Row {
	sortedProfiles := append([]Profile{}, profiles...)
	sort.SliceStable(sortedProfiles, func(i, j int) bool { return sortedProfiles[i].Name < sortedProfiles[j].Name })
	rows := make([]Row, 0, len(list))
	for _, s := range list {
		in := func(p string) bool { return p != "" && Within(p, s.Path, fold) }
		var used, wsOf, copiers, keepers, ctxOf, origOf []string
		for _, p := range sortedProfiles {
			uses := false
			if in(p.Workspace) {
				uses = true
				if p.Clone {
					if in(filepath.Dir(p.Workspace)) {
						keepers = append(keepers, p.Name)
					}
				} else {
					wsOf = append(wsOf, p.Name)
				}
			}
			if p.Clone && in(p.CloneOf) {
				uses = true
				copiers = append(copiers, p.Name)
				origOf = append(origOf, p.Name)
			}
			for _, c := range p.ContextPaths {
				if in(c) {
					uses = true
					ctxOf = append(ctxOf, p.Name)
					break
				}
			}
			if uses {
				used = append(used, p.Name)
			}
		}
		var origin string
		switch {
		case len(wsOf) > 0:
			origin = "Workspace of profile " + wsOf[0]
			if len(copiers) > 0 {
				verb := " copies it"
				if len(copiers) > 1 {
					verb = " copy it"
				}
				origin += "; " + strings.Join(copiers, ", ") + verb
			}
		case len(keepers) > 0:
			origin = "Where " + keepers[0] + " keeps its copies"
		case len(ctxOf) > 0:
			origin = "Context path of profile " + ctxOf[0]
		case len(origOf) > 0:
			origin = "Original of profile " + origOf[0]
		case anyIn(recorded, in):
			origin = "A boxed session ran here; no profile uses it"
		default:
			origin = "Added by you"
		}
		rows = append(rows, Row{Share: s, Display: Display(s.Path, home), Origin: origin, UsedBy: used})
	}
	return rows
}

func anyIn(paths []string, in func(string) bool) bool {
	for _, p := range paths {
		if in(p) {
			return true
		}
	}
	return false
}
