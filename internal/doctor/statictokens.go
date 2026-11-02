package doctor

import (
	"errors"
	"net/netip"

	"github.com/culiops/culibox/internal/config"
	"github.com/culiops/culibox/internal/credref"
)

// checkStaticTokens reports one line per static_tokens entry: whether its value
// reference resolves, never the value. doctor reads its own shell's
// environment, which is not the daemon's (DEV's comes from systemd's
// EnvironmentFile), so an unset env reference is a WARN, never a FAIL. A
// private egress range wider than a service needs adds a WARN of its own.
func checkStaticTokens(c config.Config, r credref.Resolver) []Result {
	var out []Result
	for _, e := range c.StaticTokens {
		check := "token " + e.Name
		_, err := r.Resolve(e.Value)
		switch {
		case err == nil:
			out = append(out, Result{Pass, check, e.Value.String() + " resolves"})
		case errors.Is(err, credref.ErrEnvNotSet):
			out = append(out, Result{Warn, check, e.Value.Var + " is not set in this shell; the daemon reads its own environment"})
		case errors.Is(err, credref.ErrLocked):
			// The same state the store row already WARNs about; a locked store
			// after every reboot must not turn doctor red.
			out = append(out, Result{Warn, check, "store locked"})
		default:
			out = append(out, Result{Fail, check, err.Error()})
		}
		for _, h := range e.Egress {
			if h.Private && wideTokenCIDR(h.CIDR) {
				out = append(out, Result{Warn, check, e.Name + ": private cidr " + h.CIDR + " is wide; make it as narrow as the service"})
			}
		}
	}
	return out
}

// wideTokenCIDR reports an IPv4 range shorter than /16 or an IPv6 range
// shorter than /48. An unparsable value is not wide: config validation owns it.
func wideTokenCIDR(cidr string) bool {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return false
	}
	if p.Addr().Is4() {
		return p.Bits() < 16
	}
	return p.Bits() < 48
}
