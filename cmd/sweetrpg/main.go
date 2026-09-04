package main

import (
	"errors"
	"os"

	"github.com/sweetrpg/sweetrpg-cli/internal/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		var ec cmd.ExitCoder
		if errors.As(err, &ec) {
			os.Exit(ec.ExitCode())
		}
		os.Exit(1)
	}
}
