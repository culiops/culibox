package egress

// MaxProxyConns caps the proxy's concurrently open client connections: twice the
// tunnel cap, so every allowed tunnel still fits and the rest is headroom for
// requests being authenticated. Without it any box on the egress network could
// open thousands of unauthenticated connections, each held up to
// ReadHeaderTimeout, and run the daemon out of file descriptors. The cap itself
// is netlimit.Listener.
const MaxProxyConns = 512
