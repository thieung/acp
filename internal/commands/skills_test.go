package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

func TestSkillsSetPreviewsThenApplies(t *testing.T) {
	f := testkit.New(t)
	mustOK(t, acp(t, f, "", "sync", "--all"))
	f.Write(filepath.Join(f.CP, "skills/extra/SKILL.md"), "---\nname: extra\n---\n")
	manifest := filepath.Join(f.CP, "projects/demo.json")
	before := f.Read(manifest)
	code, data := acpJSON(t, f, "skills", "set", "demo", "--add", "extra", "--remove", "ak-cook")
	if code != 0 || data["applied"] != false {
		t.Fatalf("preview: %d %v", code, data)
	}
	if f.Read(manifest) != before {
		t.Fatal("a preview writes nothing")
	}
	diff := strings.Join(toStrings(data["diff"]), "\n")
	if !strings.Contains(diff, "--- projects/demo.json") || !strings.Contains(diff, `+      "name": "extra"`) {
		t.Fatalf("diff:\n%s", diff)
	}
	planned := false
	for _, l := range data["plan"].([]any) {
		for _, c := range toStrings(l.(map[string]any)["changes"]) {
			planned = planned || strings.Contains(c, "+ .claude/skills/extra")
		}
	}
	if !planned {
		t.Fatalf("plan: %v", data["plan"])
	}
	code, data = acpJSON(t, f, "skills", "set", "demo", "--add", "extra", "--remove", "ak-cook", "--apply")
	if code != 0 || data["applied"] != true {
		t.Fatalf("apply: %d %v", code, data)
	}
	m := readMap(t, manifest)
	if ex := m["agentkit"].(map[string]any)["exclude"]; !reflect.DeepEqual(ex, []any{"ak-cook"}) {
		t.Fatalf("exclude %v", ex)
	}
	found := false
	for _, s := range m["skills"].([]any) {
		found = found || reflect.DeepEqual(s, map[string]any{"name": "extra", "runtimes": []any{"claude", "codex"}})
	}
	if !found || !f.IsLink(filepath.Join(f.Repo, ".claude/skills/extra")) {
		t.Fatalf("skills %v", m["skills"])
	}
	calls := strings.Split(strings.TrimSpace(f.Read(filepath.Join(f.Repo, ".ak-calls"))), "\n")
	if last := calls[len(calls)-1]; !strings.Contains(last, "kit init engineer --fresh") {
		t.Fatalf("last ak call %q", last)
	}
	if r := acp(t, f, "", "doctor"); r.code != 0 {
		t.Fatalf("doctor:\n%s", r.stdout)
	}
	r := mustOK(t, acp(t, f, "", "skills", "set", "demo", "--add", "extra"))
	if !strings.Contains(r.stdout, "nothing to change") {
		t.Fatalf("no-op set:\n%s", r.stdout)
	}
}

func TestSkillsSetRejectsUnknownSkillsAndRuntimes(t *testing.T) {
	f := testkit.New(t)
	for _, args := range [][]string{
		{"skills", "set", "demo", "--add", "nope"},
		{"skills", "set", "demo", "--add", "herdr", "--runtimes", "vim"},
		{"skills", "set", "demo", "--add", "../x"},
		{"skills", "set", "demo"},
	} {
		if r := acp(t, f, "", args...); r.code == 0 {
			t.Errorf("%v accepted:\n%s", args, r.stdout)
		}
	}
}

func TestSkillsListShowsCatalogs(t *testing.T) {
	f := testkit.New(t)
	code, data := acpJSON(t, f, "skills", "list", "demo")
	if code != 0 {
		t.Fatalf("exit %d %v", code, data)
	}
	personal := data["personal"].(map[string]any)
	if !reflect.DeepEqual(personal["catalog"], []any{"herdr"}) ||
		!reflect.DeepEqual(personal["selected"], map[string]any{"herdr": []any{"claude", "codex"}}) {
		t.Fatalf("personal %v", personal)
	}
	if ak := data["agentkit"].(map[string]any); ak["kit"] != "engineer" || ak["mode"] != "all" {
		t.Fatalf("agentkit %v", ak)
	}
}

func TestKitAddPreviewsThenInstalls(t *testing.T) {
	f := testkit.New(t)
	bare := filepath.Join(f.Base, "bare")
	os.MkdirAll(bare, 0o755)
	f.Manifest("bare", `{"id": "bare", "skills": []}`)
	f.Overlay(map[string]string{"demo": f.Repo, "bare": bare})
	mustOK(t, acp(t, f, "", "kit", "add", "bare", "--targets", "claude-code"))
	if readMap(t, filepath.Join(f.CP, "projects/bare.json"))["agentkit"] != nil {
		t.Fatal("preview wrote the manifest")
	}
	mustOK(t, acp(t, f, "", "kit", "add", "bare", "--targets", "claude-code", "--apply"))
	if ak := readMap(t, filepath.Join(f.CP, "projects/bare.json"))["agentkit"].(map[string]any); ak["kit"] != "engineer" {
		t.Fatalf("agentkit %v", ak)
	}
	if !f.Exists(filepath.Join(bare, ".claude/skills/ak-cook/SKILL.md")) {
		t.Fatal("kit add did not install")
	}
	if r := acp(t, f, "", "kit", "add", "demo"); r.code != 1 || !strings.Contains(r.stderr, "already uses AgentKit") {
		t.Fatalf("kit add on an AgentKit project: %d %q", r.code, r.stderr)
	}
	if r := acp(t, f, "", "kit", "add", "bare", "--targets", "vim"); r.code == 0 {
		t.Fatal("unknown target accepted")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, it := range v.([]any) {
		out = append(out, it.(string))
	}
	return out
}
