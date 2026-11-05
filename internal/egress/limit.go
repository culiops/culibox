package egress

// MaxProxyConns caps the proxy's concurrently open client connections: twice the
// tunnel cap, so every allowed tunnel still fits and the rest is headroom for
// requests being authenticated. Without it any box on the egress network could
// open thousands of unauthenticated connections, each held up to
// ReadHeaderTimeout, and run the daemon out of file descriptors. The cap itself
// is netlimit.Listener.
const MaxProxyConns = 512

// MaxProxyConnsPerSource caps the open connections any one box may hold: a
// quarter of MaxProxyConns, the same ratio as the broker's per-box cap (64 of
// 256). Without it one box, even with only its own tunnels and half-open
// requests, could take every slot of MaxProxyConns and stall every other box's
// egress. A session's tunnel cap is 32, so a box keeps its full tunnel quota
// for up to four sessions. The source is the PROXY line's address on macOS and
// the peer's own address on Linux; the cap is netlimit.PerSource, above the
// PROXY wrapper, so a connection that has not shown its source yet is held by
// the total cap alone. A box over its cap has the extra connections closed, with
// no log line and no audit row, as the broker's are. Decided, not measured.
const MaxProxyConnsPerSource = 128
