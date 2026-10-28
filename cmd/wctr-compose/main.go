// Command wctr-compose is `wctr compose` on its own: the same command
// tree (package cli), until the engine's wctr wires it as a subcommand.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/go-wasmcontainers/compose/cli"
	"github.com/spf13/cobra"
)

// osExit is replaced by the tests.
var osExit = os.Exit

// RootCmd returns the command, named wctr-compose.
func RootCmd() *cobra.Command {
	cmd := cli.Command(cli.Options{})
	cmd.Use = "wctr-compose"
	return cmd
}

func run(args []string, stdout, stderr io.Writer) int {
	cmd := RootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(stderr, "wctr compose:", err)
		return 1
	}
	return 0
}

func main() { osExit(run(os.Args[1:], os.Stdout, os.Stderr)) }
