// Command acp syncs AgentKit and personal skills across a fleet of repos and their worktrees.
package main

import (
	"os"

	"github.com/thieung/acp/internal/commands"
)

func main() {
	os.Exit(commands.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
