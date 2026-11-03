package commands

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/mcpserver"
)

// cmdMCP serves the read-and-preview tools on stdio until the client closes stdin.
func cmdMCP(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	bin, err := os.Executable()
	if err != nil {
		return 0, fail("cannot find the acp binary: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := mcpserver.New(root, Version, core.SelfRunner(bin, e.Cwd))
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		return 0, fail("mcp: %v", err)
	}
	return 0, nil
}
