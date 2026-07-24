package web

import "github.com/culiops/culiops-agent-console/internal/config"

// ProfileOption is one option a profile exposes, with its default value, for the
// spawn-form UI. It carries no secret names/values or flag/env mapping.
type ProfileOption struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`
	Choices []string `json:"choices,omitempty"`
	Value   any      `json:"value"` // default
}

// ProfileView is a profile plus its exposed options, for GET /api/profiles.
type ProfileView struct {
	Name    string          `json:"name"`
	Options []ProfileOption `json:"options,omitempty"`
}

// Profiles builds the UI view from config. Options are ordered by their config
// definition order (stable UI), and only those a profile actually exposes appear.
func Profiles(c config.Config) []ProfileView {
	out := make([]ProfileView, 0, len(c.Agents))
	for _, a := range c.Agents {
		pv := ProfileView{Name: a.Name}
		for _, def := range c.Options {
			if v, ok := a.Options[def.Key]; ok {
				pv.Options = append(pv.Options, ProfileOption{
					Key: def.Key, Label: def.Label, Type: def.Type, Choices: def.Choices, Value: v,
				})
			}
		}
		out = append(out, pv)
	}
	return out
}
