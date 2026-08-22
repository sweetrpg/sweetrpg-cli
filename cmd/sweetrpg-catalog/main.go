package main

import (
	"os"

	"github.com/sweetrpg/catalog-cli/internal/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
