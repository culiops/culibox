package doctor

// checkConfigFileWarn is the WARN row for a config read from the culibox
// ~/.config file while the data root is still the pre-rename one (#266). It
// reads only the Source that config.Resolve filled in.
func checkConfigFileWarn(in Input) []Result {
	if w := in.Source.UnvettedWarning(in.Start); w != "" {
		return []Result{{Warn, "config", w}}
	}
	return nil
}
