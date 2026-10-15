//go:build darwin

package main

import (
	"fmt"
	"io"

	"github.com/culiops/agent-box/internal/config"
)

func runBoxDarwin(a cliArgs, cfg config.Config, home string, w io.Writer) int {
	fmt.Fprintln(w, "agent-box box: macOS support lands with the box VM (this build predates it)")
	return 1
}
