package main

import (
	"os"

	"github.com/zeropsio/zbox-cli/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
