//go:build !darwin

package main

import (
	"io"

	"github.com/culiops/agent-box/internal/config"
)

func runBoxDarwin(cliArgs, config.Config, string, io.Writer) int { return 1 } // unreachable off darwin
