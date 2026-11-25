package boximagecheck

import (
	"fmt"
	"math"
	"slices"
	"time"

	boximage "github.com/culiops/culibox/docker/culibox"
	"github.com/culiops/culibox/internal/boxregistry"
)

// Source says where Report.Want came from.
type Source string

const (
	SourceBaked    Source = "baked"    // the digest a release binary carries
	SourceRegistry Source = "registry" // the registry's answer, cached
)

// Verdict is how the running image compares with the image for this version.
type Verdict string

const (
	Current      Verdict = "current"       // the running image is the published one
	CurrentLocal Verdict = "current-local" // built here from this version's Dockerfile
	Update       Verdict = "update"        // an image for this version exists and is not the running one
	StaleUnknown Verdict = "stale-unknown" // older Dockerfile, and whether one was published is not known
	Missing      Verdict = "missing"       // the daemon does not hold the image
	Unknown      Verdict = "unknown"       // the daemon could not be asked
)

// Inputs is everything Evaluate decides from. It reads no clock, file, network or
// global, so a caller's facts decide the verdict alone.
type Inputs struct {
	Configured string              // cfg.BoxImageOrDefault()
	Explicit   bool                // cfg.BoxImage != ""
	Published  string              // boximage.Published()
	CheckOn    bool                // cfg.ImageCheckOn()
	Arch       string              // the daemon's arch, "" when unknown
	Info       *boximage.ImageInfo // nil when the daemon could not be asked
	Cache      *Cache              // nil when none
	CacheNewer bool                // the cache file is from a newer CuliBox (LoadCache said datacompat.ErrNewer)
}

// Report is the verdict and the facts it rests on, as the UI and doctor show them.
type Report struct {
	Configured     string              `json:"configured"`
	Explicit       bool                `json:"explicit"`
	Info           *boximage.ImageInfo `json:"info,omitempty"`
	Want           string              `json:"want,omitempty"`
	WantSource     Source              `json:"want_source,omitempty"`
	WantCreated    time.Time           `json:"want_created,omitzero"`
	WantClaudeCode string              `json:"want_claude_code,omitempty"`
	Verdict        Verdict             `json:"verdict"`
	DownloadMB     string              `json:"download_mb,omitempty"` // formatted, "" when unknown or nothing to pull
	ConfigLine     string              `json:"config_line,omitempty"`
	SetupCmd       string              `json:"setup_cmd,omitempty"`
	CheckOn        bool                `json:"check_on"`
	CheckedAt      time.Time           `json:"checked_at,omitzero"`
	CheckErr       string              `json:"check_err,omitempty"`
	Scan           *Scan               `json:"scan,omitempty"`
}

// NeedsNetwork reports whether this instance may ask the registry at all. A
// release binary running its default image already carries the answer and calls
// nothing; an explicit box_image or a source build has nothing baked to compare with.
func NeedsNetwork(in Inputs) bool {
	return in.CheckOn && (in.Explicit || in.Published == "")
}

// Evaluate decides which image is this version's and how the running one compares.
func Evaluate(in Inputs) Report {
	r := Report{Configured: in.Configured, Explicit: in.Explicit, Info: in.Info, CheckOn: in.CheckOn}

	// The cache speaks only for this binary's Dockerfile, and only where a call is
	// allowed: one left over from a config that has since turned the check off, or
	// from another binary, must not show its time, error, scan or registry answer.
	var plat boxregistry.Platform
	if c := in.Cache; NeedsNetwork(in) && c != nil && c.Hash == boximage.Hash() {
		r.CheckedAt, r.CheckErr, r.Scan = c.CheckedAt, c.RegistryErr, c.Scan
		if c.Registry != nil && c.Registry.Ref != "" {
			r.Want, r.WantSource = c.Registry.Ref, SourceRegistry
			var ok bool
			if plat, ok = c.Registry.Platforms[in.Arch]; ok {
				r.WantCreated, r.WantClaudeCode = plat.Created, plat.ClaudeCode
			}
		}
	}
	if in.CacheNewer && NeedsNetwork(in) && in.Cache == nil {
		r.CheckErr = ErrClassNewer
	}
	if r.Want == "" && in.Published != "" {
		r.Want, r.WantSource = in.Published, SourceBaked
	}

	switch {
	case in.Info == nil:
		r.Verdict = Unknown
	case !in.Info.Present:
		r.Verdict = Missing
	case r.Want != "" && (slices.Contains(in.Info.RepoDigests, r.Want) || in.Configured == r.Want):
		r.Verdict = Current
	case in.Info.Hash == boximage.Hash():
		r.Verdict = CurrentLocal
	case r.Want != "":
		r.Verdict = Update
	default:
		r.Verdict = StaleUnknown
	}

	if r.Verdict == Update {
		r.SetupCmd = "culibox box setup"
		// The release default pulls the baked digest by itself; any other instance has
		// to name the image in its config first.
		if in.Explicit || in.Published == "" {
			r.ConfigLine = fmt.Sprintf(`"box_image": %q`, r.Want)
		}
		if r.WantSource == SourceRegistry {
			r.DownloadMB = downloadMB(plat, in.Info.DiffIDs)
		}
	}
	return r
}

// downloadMB is the compressed size of the published layers the running image does
// not hold, formatted for display, or "" when it cannot be said (no layer list on
// either side) or there is nothing to pull. Sizes are capped per layer by
// boxregistry; the sum is int64 and cannot wrap for any real layer count.
func downloadMB(p boxregistry.Platform, have []string) string {
	if p.DiffIDs == nil || len(p.DiffIDs) != len(p.Layers) || len(have) == 0 {
		return ""
	}
	var missing int64
	for i, id := range p.DiffIDs {
		if !slices.Contains(have, id) {
			missing += p.Layers[i].Size
		}
	}
	mb := float64(missing) / 1e6
	if mb < 0.05 { // would print "0.0": not worth stating
		return ""
	}
	if mb < 9.95 { // one decimal below 10, but 9.96 must not print as "10.0"
		return fmt.Sprintf("%.1f", mb)
	}
	return fmt.Sprintf("%.0f", math.Round(mb))
}
