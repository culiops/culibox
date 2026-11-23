package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/culiops/culibox/internal/box"
)

// keptHomesTimeout bounds the size walks of the kept homes. Their caches are
// pruned an hour after their last use, so what is left is usually small.
const keptHomesTimeout = 10 * time.Second

// checkKeptHomes lists the box homes reclaim keeps past box.DefaultStateTTL
// because they hold Claude Code conversations (#330), with their sizes, so the
// disk they hold is visible and removing one is the user's choice. No row when
// there is none.
func checkKeptHomes(dataDir string) []Result {
	const check = "kept box homes"
	ctx, cancel := context.WithTimeout(context.Background(), keptHomesTimeout)
	defer cancel()
	homes, err := box.KeptHomes(ctx, dataDir, box.DefaultStateTTL)
	if len(homes) == 0 {
		if err != nil && ctx.Err() == nil {
			return []Result{{Warn, check, err.Error()}}
		}
		return nil
	}
	parts := make([]string, 0, len(homes))
	for _, h := range homes {
		size := "size not measured"
		if h.Sized {
			size = fmt.Sprintf("%.1f MiB", float64(h.Bytes)/(1024*1024))
		}
		parts = append(parts, fmt.Sprintf("%s (unused %d days, %s) at %s", h.Box, int(h.Age.Hours()/24), size, h.Dir))
	}
	noun := "homes"
	if len(homes) == 1 {
		noun = "home"
	}
	more := ""
	if err != nil {
		more = " (the list may be incomplete: ran out of time)"
	}
	return []Result{{Pass, check, fmt.Sprintf("%d %s unused for over %d days kept because each holds conversations%s: %s. "+
		"To remove one, check that `culibox box status` lists no box by that name, then delete its folder and its out dir (%s).",
		len(homes), noun, int(box.DefaultStateTTL.Hours()/24), more, strings.Join(parts, "; "), filepath.Join(filepath.Dir(homes[0].OutDir), "<name>"))}}
}
