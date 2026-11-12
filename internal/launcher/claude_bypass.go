package launcher

// ArgsBypassPermissions reports whether args turn Claude Code's permission
// checks off: the bare --dangerously-skip-permissions flag, or
// --permission-mode bypassPermissions (two args or one). Claude Code shows its
// one-time "Bypass Permissions mode" warning for either. An unrecognised
// spelling reads as "not bypassing", so the answer errs toward the dialog
// staying, never toward consent that was not given.
func ArgsBypassPermissions(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--dangerously-skip-permissions", a == "--permission-mode=bypassPermissions":
			return true
		case a == "--permission-mode" && i+1 < len(args) && args[i+1] == "bypassPermissions":
			return true
		}
	}
	return false
}

// BypassesPermissions reports whether a spawn runs its agent with the bypass
// option: the flags come from the same merge Build uses (the profile's args, then
// its option defaults with the per-spawn overrides it accepts), so the answer is
// what will actually run. spec.ExtraArgs are the daemon's own flags (bundle,
// MCP) and never carry it. An unknown agent does not bypass.
//
// The box layer uses it to pre-accept the warning in a new box home: the option
// the user chose for the profile or the spawn is the consent.
func (l *AgentLauncher) BypassesPermissions(spec SpawnSpec) bool {
	cfg, ok := l.agent(spec.Agent)
	if !ok {
		return false
	}
	cmd := append([]string{}, cfg.Args...)
	cmd = l.applyOptionFlags(l.mergeOptions(cfg, spec), cmd)
	return ArgsBypassPermissions(cmd)
}
