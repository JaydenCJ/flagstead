// Command flagstead is a single-binary feature-flag and remote-config
// server backed by one TOML file. All logic lives in internal packages;
// this entry point only wires argv and exit codes.
package main

import (
	"os"

	"github.com/JaydenCJ/flagstead/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
