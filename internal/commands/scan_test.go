package commands

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/scan"
	"github.com/thieung/acp/internal/testkit"
)

func TestScanJSONAndText(t *testing.T) {
	f := testkit.New(t)
	code, data := acpJSON(t, f, "scan")
	if code != 0 {
		t.Fatalf("exit %d %v", code, data)
	}
	found := map[string]map[string]any{}
	for _, it := range data["findings"].([]any) {
		fd := it.(map[string]any)
		found[fd["code"].(string)] = fd
	}
	wt := found["worktree_unlinked"]
	if wt == nil || wt["title"] == "" || wt["todo"] == "" {
		t.Fatalf("findings %v", found)
	}
	fix := wt["fix"].(map[string]any)
	if fix["preview"] != "acp link "+f.WT+" --check" || fix["safety"] != "safe" {
		t.Fatalf("fix %v", fix)
	}
	if _, ok := data["summary"].(map[string]any)["hooks"]; !ok {
		t.Fatalf("summary %v", data["summary"])
	}
	r := mustOK(t, acp(t, f, "", "scan", "--module", "projects"))
	if !strings.Contains(r.stdout, "Worktree "+f.WT+" chưa được gắn skill") || !strings.Contains(r.stdout, "acp link "+f.WT+" --check") {
		t.Fatalf("text:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "ak_") {
		t.Fatalf("--module projects shows agentkit findings:\n%s", r.stdout)
	}
	if f.Exists(filepath.Join(f.WT, ".claude/skills/herdr")) {
		t.Fatal("scan linked something")
	}
}

func TestExplain(t *testing.T) {
	f := testkit.New(t)
	code, data := acpJSON(t, f, "explain", "worktree_unlinked")
	if code != 0 || data["title"] == "" || len(data["glossary"].([]any)) == 0 {
		t.Fatalf("exit %d %v", code, data)
	}
	r := mustOK(t, acp(t, f, "", "explain", "pin_missing"))
	for _, want := range []string{"Chuyện gì", "Vì sao", "Nếu bỏ qua", "Nên làm gì", "kênh Beta"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("%q missing:\n%s", want, r.stdout)
		}
	}
	if r := acp(t, f, "", "explain", "nope"); r.code != 1 || !strings.Contains(r.stderr, "unknown finding code") {
		t.Fatalf("unknown code: %d %q", r.code, r.stderr)
	}
}

func TestEveryFixKindIsADescribedMutation(t *testing.T) {
	byName := map[string]contract{}
	for _, c := range contracts {
		byName[c.name] = c
	}
	for kind, name := range scan.FixKinds {
		c, ok := byName[name]
		if !ok || !c.mutation || c.preview == "" {
			t.Errorf("fix kind %s -> %q is not a described mutation with a preview", kind, name)
		}
		args := []string{"x"}
		if kind == "upgrade" {
			args = []string{"--channel", "beta", "--kit", "engineer"}
		}
		fix := &scan.Fix{Kind: kind, Args: args}
		if len(fix.Argv()) == 0 || len(fix.PreviewArgv()) == 0 {
			t.Errorf("fix kind %s has no command", kind)
		}
		if _, err := parse(fix.PreviewArgv()); err != nil {
			t.Errorf("fix kind %s preview %v does not parse: %v", kind, fix.PreviewArgv(), err)
		}
		if _, err := parse(fix.Argv()); err != nil {
			t.Errorf("fix kind %s command %v does not parse: %v", kind, fix.Argv(), err)
		}
	}
}
