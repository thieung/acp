package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/testkit"
)

// acpBin is the acp binary the server runs for status and fix previews, as it does in production.
var acpBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "acp-mcp-bin")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	acpBin = filepath.Join(dir, "acp")
	if out, err := exec.Command("go", "build", "-o", acpBin, "github.com/thieung/acp/cmd/acp").CombinedOutput(); err != nil {
		fmt.Printf("build acp: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func connect(t *testing.T, f *testkit.Fixture) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := New(&core.Root{Dir: f.CP}, "test", core.SelfRunner(acpBin, f.Base))
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String(), res.IsError
}

func TestToolsAreTheFourReadOnlyOnes(t *testing.T) {
	cs := connect(t, testkit.New(t))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not marked read-only", tool.Name)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "acp_explain,acp_fix_preview,acp_scan,acp_status" {
		t.Fatalf("tools %v", names)
	}
}

func TestExplain(t *testing.T) {
	cs := connect(t, testkit.New(t))
	if text, isErr := call(t, cs, "acp_explain", map[string]any{"code": "nope"}); !isErr || !strings.Contains(text, "unknown finding code") {
		t.Fatalf("unknown code: %v %s", isErr, text)
	}
	text, isErr := call(t, cs, "acp_explain", map[string]any{"code": "worktree_unlinked"})
	var card map[string]any
	if isErr || json.Unmarshal([]byte(text), &card) != nil || card["title"] == "" || card["todo"] == "" {
		t.Fatalf("explain: %v %s", isErr, text)
	}
}

func TestFixPreviewChangesNothing(t *testing.T) {
	f := testkit.New(t)
	cs := connect(t, f)
	before := testkit.Fingerprint(f.Base)

	text, isErr := call(t, cs, "acp_scan", map[string]any{"module": "projects"})
	var report struct {
		Findings []struct{ ID, Code string } `json:"findings"`
	}
	if isErr || json.Unmarshal([]byte(text), &report) != nil {
		t.Fatalf("scan: %v %s", isErr, text)
	}
	id := ""
	for _, fd := range report.Findings {
		if fd.Code == "worktree_unlinked" {
			id = fd.ID
		}
	}
	if id == "" {
		t.Fatalf("no worktree_unlinked in %s", text)
	}
	text, isErr = call(t, cs, "acp_fix_preview", map[string]any{"finding_id": id})
	var prev map[string]any
	if isErr || json.Unmarshal([]byte(text), &prev) != nil {
		t.Fatalf("preview: %v %s", isErr, text)
	}
	if prev["command"] != "acp link "+f.WT || !strings.Contains(fmt.Sprint(prev["preview"]), ".claude/skills/herdr") {
		t.Fatalf("preview %s", text)
	}
	if _, isErr := call(t, cs, "acp_fix_preview", map[string]any{"finding_id": "nope-00000000"}); !isErr {
		t.Fatal("unknown finding id is not a tool error")
	}
	if text, isErr := call(t, cs, "acp_status", nil); isErr || !strings.Contains(text, "projects") {
		t.Fatalf("status: %v %s", isErr, text)
	}

	after := testkit.Fingerprint(f.Base)
	for p, v := range before {
		if after[p] != v {
			t.Errorf("changed: %s", p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("created: %s", p)
		}
	}
}

func TestScanOutputHasNoSecrets(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.Home, ".claude.json"), `{"mcpServers": {
  "gone": {"type": "stdio", "command": "/opt/missing/server", "args": ["--key", "secret-x"], "env": {"GITHUB_TOKEN": "ghp_x"}},
  "remote": {"type": "http", "url": "https://mcp.example.com", "headers": {"Authorization": "Bearer secret-x"}}}}`)
	cs := connect(t, f)
	text, isErr := call(t, cs, "acp_scan", nil)
	if isErr || !json.Valid([]byte(text)) || !strings.Contains(text, "mcp_command_missing") {
		t.Fatalf("scan: %v %s", isErr, text)
	}
	for _, secret := range []string{"secret-x", "ghp_x", "mcp.example.com"} {
		if strings.Contains(text, secret) {
			t.Fatalf("scan output contains %q", secret)
		}
	}
	if _, isErr := call(t, cs, "acp_scan", map[string]any{"module": "nope"}); !isErr {
		t.Fatal("unknown module is not a tool error")
	}
}
