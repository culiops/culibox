package doctor

import (
	"fmt"
	"strings"

	"github.com/culiops/culibox/internal/box"
	"github.com/culiops/culibox/internal/config"
)

// keptStoreWarnBytes is the size past which a kept-folder store is reported.
const keptStoreWarnBytes = int64(5) << 30

// checkKeptStores reports the kept box folders (the folders a boxed agent wrote
// in its home, kept per profile): how many stores, their total size and where.
// Sizes come from each store's state.json; nothing is walked. It warns when a
// store is over 5 GiB, when a store's profile no longer exists, and when a
// store's state names no profile (it does not hash to its folder). profiles is
// the set of profile names doctor knows; nil means doctor has no list, and a
// named profile is then not judged. No row when there is no store.
func checkKeptStores(dataDir, instance string, profiles map[string]bool) []Result {
	const check = "kept box folders"
	views, err := box.ReadKeptStores(dataDir, instance)
	if len(views) == 0 {
		if err != nil {
			return []Result{{Warn, check, err.Error()}}
		}
		return nil
	}
	var total int64
	var warns []string
	for _, v := range views {
		total = satAdd(total, v.TotalBytes)
		switch {
		case v.Profile == "":
			warns = append(warns, fmt.Sprintf("store %s names no profile (its state does not match its folder); check it, then delete the folder if it is not yours", v.Key))
		case profiles != nil && !profiles[v.Profile]:
			warns = append(warns, fmt.Sprintf("store %s belongs to profile %q, which no longer exists; delete the folder to free its disk", v.Key, v.Profile))
		}
		if v.TotalBytes > keptStoreWarnBytes {
			warns = append(warns, fmt.Sprintf("store %s is %.1f GiB, over 5 GiB; open the profile and Forget what you do not need", v.Key, float64(v.TotalBytes)/(1<<30)))
		}
	}
	noun := "stores"
	if len(views) == 1 {
		noun = "store"
	}
	detail := fmt.Sprintf("%d %s (one per profile and workspace), %.1f MiB in all, in %s",
		len(views), noun, float64(total)/(1024*1024), box.KeptRoot(dataDir))
	status := Pass
	if err != nil {
		warns = append(warns, "the list may be incomplete: "+err.Error())
	}
	if len(warns) > 0 {
		status = Warn
		detail += ". " + strings.Join(warns, "; ")
	}
	return []Result{{status, check, detail}}
}

func satAdd(a, b int64) int64 {
	if a > 0 && b > 1<<62-a {
		return 1<<62 - 1
	}
	return a + b
}

// profileNameSet is the profile names doctor reads: the config's agents and the
// profiles store's. nil when the profiles store could not be read (no list, so
// no orphan judgment).
func profileNameSet(c config.Config, stored []config.AgentConfig, storeErr error) map[string]bool {
	if storeErr != nil {
		return nil
	}
	set := map[string]bool{}
	for _, a := range c.Agents {
		set[a.Name] = true
	}
	for _, a := range stored {
		set[a.Name] = true
	}
	return set
}
