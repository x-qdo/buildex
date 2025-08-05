package main

import (
	"os"

	"github.com/x-qdo/buildex/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
