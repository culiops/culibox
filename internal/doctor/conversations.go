package doctor

import (
	"context"
	"fmt"

	"github.com/culiops/culibox/internal/box"
)

// checkConversations reports the conversations folders boxes share per profile
// and workspace (#330): how many, their size and where. They are never
// reclaimed, so their disk is shown here. No row when there is none.
func checkConversations(dataDir string) []Result {
	const check = "conversations folders"
	ctx, cancel := context.WithTimeout(context.Background(), keptHomesTimeout)
	defer cancel()
	st, err := box.ConversationsUsage(ctx, dataDir)
	if st.Folders == 0 {
		if err != nil && ctx.Err() == nil {
			return []Result{{Warn, check, err.Error()}}
		}
		return nil
	}
	noun := "folders"
	if st.Folders == 1 {
		noun = "folder"
	}
	size := "size not fully measured (time or depth limit)"
	if st.Sized {
		size = fmt.Sprintf("%.1f MiB", float64(st.Bytes)/(1024*1024))
	}
	return []Result{{Pass, check, fmt.Sprintf("%d %s (one per profile and workspace, kept: reclaim never removes them), %s, in %s",
		st.Folders, noun, size, box.ConversationsRoot(dataDir))}}
}
