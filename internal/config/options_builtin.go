package config

// builtinOptions is every agent's built-in launch options, one list per
// provider; a later provider adds its own list here.
var builtinOptions = [][]OptionDef{claudeBuiltinOptions}

// sameSlot are keys the UI shows in one slot: declaring either one takes the
// slot, so a built-in for the other is not added next to it.
var sameSlot = map[string]string{"effortLevel": "effort", "effort": "effortLevel"}

// withBuiltinOptions is the declared options (config.json, in order) followed
// by every built-in whose key is not declared. A declared key replaces the
// built-in as a whole, so config.json can always override one.
func withBuiltinOptions(declared []OptionDef) []OptionDef {
	taken := make(map[string]bool, len(declared))
	for _, o := range declared {
		taken[o.Key] = true
		if other, ok := sameSlot[o.Key]; ok {
			taken[other] = true
		}
	}
	out := append([]OptionDef(nil), declared...)
	for _, list := range builtinOptions {
		for _, o := range list {
			if !taken[o.Key] {
				out = append(out, o)
				taken[o.Key] = true
			}
		}
	}
	return out
}
