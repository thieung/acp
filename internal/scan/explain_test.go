package scan

import (
	"strings"
	"testing"

	"github.com/thieung/acp/internal/core"
)

func TestEveryCodeHasAnExplanation(t *testing.T) {
	for _, code := range Codes {
		e, ok := catalog.Codes[code]
		if !ok {
			t.Errorf("%s: no catalog entry", code)
			continue
		}
		for name, text := range map[string]string{"title": e.Title, "what": e.What, "why": e.Why, "ignore": e.Ignore, "todo": e.Todo} {
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s: empty %s", code, name)
			}
		}
		for _, term := range e.Glossary {
			if catalog.Glossary[term] == "" {
				t.Errorf("%s: glossary term %q is not defined", code, term)
			}
		}
	}
	if len(catalog.Codes) != len(Codes) {
		t.Errorf("catalog has %d codes, scan emits %d", len(catalog.Codes), len(Codes))
	}
	for _, term := range []string{"AgentKit", "skill", "worktree", "kênh Beta", "kênh Stable", "Claude Code", "Codex",
		"Git", "commit", "hook", "MCP", "bản sao lưu"} {
		if catalog.Glossary[term] == "" {
			t.Errorf("glossary lacks %q", term)
		}
	}
	if beta := catalog.Glossary["kênh Beta"]; beta != "Bản chưa phát hành chính thức. Các tính năng mới nhất luôn có ở đây trước, nhưng có thể gặp một số lỗi khi sử dụng." {
		t.Errorf("Beta wording changed: %q", beta)
	}
}

func TestExplainFillsParams(t *testing.T) {
	e, ok := Explain("worktree_unlinked", map[string]string{"worktree": "/w/feature", "count": "3"})
	if !ok || !strings.Contains(e.Title, "/w/feature") || !strings.Contains(e.What, "3 liên kết") {
		t.Fatalf("%+v", e)
	}
	generic, _ := Explain("worktree_unlinked", nil)
	if !strings.Contains(generic.Title, "<worktree>") {
		t.Fatalf("generic title %q", generic.Title)
	}
	if _, ok := Explain("nope", nil); ok {
		t.Fatal("unknown code explained")
	}
}

func TestFindingsAreRedacted(t *testing.T) {
	f := newFinding("hook_duplicate", Info, "hooks", "x token=abc123",
		map[string]string{"command": "curl -H 'Authorization: Bearer sk-abcdefghijklmnopqrstu' x"},
		"api_key=zzz", nil)
	out, _ := core.MarshalJSON(f.Object())
	for _, secret := range []string{"abc123", "sk-abcdefghijklmnopqrstu", "zzz"} {
		if strings.Contains(string(out), secret) {
			t.Fatalf("%q leaked:\n%s", secret, out)
		}
	}
}

func TestPluginExplanationsCarryTheClaudeCommand(t *testing.T) {
	params := map[string]string{"project": "ejar", "plugin": "pstack@cursor-plugins", "checkout": "/w/ejar",
		"command": "claude plugin install pstack@cursor-plugins --scope project", "pin_text": " bản 0.15.36"}
	e, ok := Explain("plugin_not_installed", params)
	if !ok || !strings.Contains(e.Todo, params["command"]) || !strings.Contains(e.What, "bản 0.15.36") ||
		!strings.Contains(e.Title, "pstack@cursor-plugins") {
		t.Fatalf("%+v", e)
	}
	for _, code := range []string{"plugin_not_enabled", "plugin_version_drift", "plugin_user_scope_enabled"} {
		if e, _ := Explain(code, nil); !strings.Contains(e.Todo, "<command>") {
			t.Errorf("%s todo lacks the command: %q", code, e.Todo)
		}
	}
	if e, _ := Explain("plugin_runtime_unsupported", nil); !strings.Contains(e.Todo, "runtime") {
		t.Errorf("unsupported todo %q", e.Todo)
	}
}
