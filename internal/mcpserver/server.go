// Package mcpserver is `acp mcp`: an MCP server whose tools only read and preview. A fix runs only
// when a person runs its command in `acp web` or a terminal.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/scan"
)

const instructions = `acp keeps agent skills in sync across a fleet of repos and their worktrees.
These tools only read and preview; none of them changes the machine. To fix a finding, show the
person the command from acp_fix_preview and let them run it in a terminal or in acp web.`

const cacheFor = 30 * time.Second

type server struct {
	root *core.Root
	run  core.Runner

	mu    sync.Mutex
	cache map[string]cached // module ("" for all) -> last report
}

type cached struct {
	at     time.Time
	report *scan.Report
}

var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true}

type scanIn struct {
	Module string `json:"module,omitempty" jsonschema:"only one dashboard module: projects, agentkit or hooks"`
}

type explainIn struct {
	Code string `json:"code" jsonschema:"finding code, e.g. worktree_unlinked"`
}

type previewIn struct {
	FindingID string `json:"finding_id" jsonschema:"id of a finding from acp_scan"`
}

// New builds the server for one control plane.
func New(root *core.Root, version string, run core.Runner) *mcp.Server {
	s := &server{root: root, run: run, cache: map[string]cached{}}
	srv := mcp.NewServer(&mcp.Implementation{Name: "acp", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	mcp.AddTool(srv, &mcp.Tool{Name: "acp_scan", Annotations: readOnly,
		Description: "Health check of projects, worktrees, AgentKit, hooks, MCP servers and user skills. Each finding has a code, a severity, a plain-language explanation and, when one exists, the acp command that fixes it. Results are cached for 30 seconds."},
		s.scanTool)
	mcp.AddTool(srv, &mcp.Tool{Name: "acp_status", Annotations: readOnly,
		Description: "Fleet status: each project's AgentKit version, personal skill links and worktrees."},
		s.statusTool)
	mcp.AddTool(srv, &mcp.Tool{Name: "acp_explain", Annotations: readOnly,
		Description: "What a finding code means: what happened, why it matters, what happens if it is ignored, what to do, and the terms it uses."},
		s.explainTool)
	mcp.AddTool(srv, &mcp.Tool{Name: "acp_fix_preview", Annotations: readOnly,
		Description: "Dry run of the fix for a finding from acp_scan: what it would change and the exact command a person runs to apply it. Changes nothing."},
		s.previewTool)
	return srv
}

func (s *server) report(ctx context.Context, module string) *scan.Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[module]; ok && time.Since(c.at) < cacheFor {
		return c.report
	}
	r := scan.Run(ctx, s.root, scan.Options{Module: module})
	if ctx.Err() == nil { // a cancelled call's checks timed out early; do not serve that for 30 s
		s.cache[module] = cached{time.Now(), r}
	}
	return r
}

func raw(o any) (json.RawMessage, error) {
	b, err := core.MarshalJSON(o)
	return json.RawMessage(b), err
}

func (s *server) scanTool(ctx context.Context, _ *mcp.CallToolRequest, in scanIn) (*mcp.CallToolResult, any, error) {
	if in.Module != "" && !slices.Contains(scan.Modules, in.Module) {
		return nil, nil, fmt.Errorf("unknown module %q; use one of %v", in.Module, scan.Modules)
	}
	out, err := raw(s.report(ctx, in.Module).Object())
	return nil, out, err
}

func (s *server) explainTool(_ context.Context, _ *mcp.CallToolRequest, in explainIn) (*mcp.CallToolResult, any, error) {
	ex, ok := scan.Explain(in.Code, nil)
	if !ok {
		return nil, nil, fmt.Errorf("unknown finding code %q; acp_scan shows the codes it found", in.Code)
	}
	out, err := raw(ex.Object())
	return nil, out, err
}

// acp runs one acp command with --json and returns its data, or its error as a tool error.
func (s *server) acp(ctx context.Context, args ...string) (map[string]any, int, error) {
	stdout, err := s.run(ctx, nil, append(args, "--json")...)
	if err != nil {
		return nil, 0, err
	}
	var env struct {
		Data     map[string]any `json:"data"`
		ExitCode int            `json:"exit_code"`
		Error    string         `json:"error"`
	}
	if err := json.Unmarshal(stdout, &env); err != nil {
		return nil, 0, fmt.Errorf("acp %s printed no JSON envelope: %v", args[0], err)
	}
	if env.Error != "" {
		return nil, env.ExitCode, fmt.Errorf("acp %s: %s", args[0], core.Redact(env.Error))
	}
	data, _ := core.RedactValue(env.Data).(map[string]any)
	return data, env.ExitCode, nil
}

func (s *server) statusTool(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	data, _, err := s.acp(ctx, "status")
	if err != nil {
		return nil, nil, err
	}
	return nil, data, nil
}

func (s *server) previewTool(ctx context.Context, _ *mcp.CallToolRequest, in previewIn) (*mcp.CallToolResult, any, error) {
	f, ok := s.finding(ctx, in.FindingID)
	if !ok {
		return nil, nil, fmt.Errorf("no finding %q in the latest scan; run acp_scan first", in.FindingID)
	}
	if f.Fix == nil {
		return nil, nil, fmt.Errorf("no acp command fixes %s; acp_explain %s says what to do by hand", f.ID, f.Code)
	}
	data, code, err := s.acp(ctx, f.Fix.PreviewArgv()...)
	if err != nil {
		return nil, nil, err
	}
	out, err := raw(core.Obj("finding_id", f.ID, "code", f.Code, "safety", f.Fix.Safety,
		"preview_command", f.Fix.Preview(), "preview_exit_code", code, "preview", data["text"],
		"command", f.Fix.Command(),
		"apply", "Only a person applies this: show them the command and let them run it in a terminal or in acp web."))
	return nil, out, err
}

// finding looks the id up in the cached reports, then in a fresh full scan.
func (s *server) finding(ctx context.Context, id string) (scan.Finding, bool) {
	s.mu.Lock()
	var reports []*scan.Report
	for _, c := range s.cache {
		reports = append(reports, c.report)
	}
	s.mu.Unlock()
	for _, r := range reports {
		for _, f := range r.Findings {
			if f.ID == id {
				return f, true
			}
		}
	}
	for _, f := range s.report(ctx, "").Findings {
		if f.ID == id {
			return f, true
		}
	}
	return scan.Finding{}, false
}
