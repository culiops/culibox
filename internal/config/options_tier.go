package config

import (
	"fmt"
	"sort"
)

// Truthy coerces a JSON-decoded value to bool without panicking on wrong types.
func Truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	default:
		return false
	}
}

// On reports whether a value turns this option on: a bool when Truthy, an enum
// when it is one of the choices and not "".
func (o OptionDef) On(v any) bool {
	switch o.Type {
	case OptBool:
		return Truthy(v)
	case OptEnum:
		s, ok := v.(string)
		return ok && s != "" && contains(o.Choices, s)
	}
	return false
}

func sortedOptionKeys(opts map[string]any) []string {
	ks := make([]string, 0, len(opts))
	for k := range opts {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// OptionFlags returns the flags the options map to, in sorted key order. A bool
// that is on appends its Flag; an enum whose value is one of the choices
// appends its Flag and the value. A key with no def or no Flag adds nothing.
func OptionFlags(defs map[string]OptionDef, opts map[string]any) []string {
	var out []string
	for _, k := range sortedOptionKeys(opts) {
		def, ok := defs[k]
		if !ok || def.Flag == "" {
			continue
		}
		switch def.Type {
		case OptBool:
			if Truthy(opts[k]) {
				out = append(out, def.Flag)
			}
		case OptEnum:
			if s, ok := opts[k].(string); ok && contains(def.Choices, s) {
				out = append(out, def.Flag, s)
			}
		}
	}
	return out
}

// StripBoxOnly returns a copy of opts without the keys whose def is box tier.
// The input is not changed.
func StripBoxOnly(defs map[string]OptionDef, opts map[string]any) map[string]any {
	out := make(map[string]any, len(opts))
	for k, v := range opts {
		if defs[k].Tier == RuntimeBox {
			continue
		}
		out[k] = v
	}
	return out
}

// BoxOnlyOptionReason names the first (sorted) box-tier option that opts turn
// on, as `"<Label>" runs only in a box`. "" when there is none.
func BoxOnlyOptionReason(defs map[string]OptionDef, opts map[string]any) string {
	for _, k := range sortedOptionKeys(opts) {
		def, ok := defs[k]
		if !ok || def.Tier != RuntimeBox || !def.On(opts[k]) {
			continue
		}
		label := def.Label
		if label == "" {
			label = def.Key
		}
		return fmt.Sprintf("%q runs only in a box", label)
	}
	return ""
}
