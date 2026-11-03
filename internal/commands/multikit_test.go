package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

// TestKitAddAndRemoveAnotherKit walks a second kit through kit add, status, skills list, upgrade,
// scan and kit remove.
func TestKitAddAndRemoveAnotherKit(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.CP, "agentkit-releases.lock.json"),
		`{"schemaVersion": 2, "channels": {"beta": {"engineer": "3.0.0-beta.36", "marketing": "3.0.0-beta.36"}}}`)
	manifest := filepath.Join(f.CP, "projects/demo.json")
	mustOK(t, acp(t, f, "", "sync", "demo"))

	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"kit", "add", "demo"}, 1, "already uses AgentKit engineer; pass --kit NAME"},
		{[]string{"kit", "add", "demo", "--kit", "engineer"}, 1, "already uses AgentKit engineer"},
		{[]string{"kit", "add", "demo", "--kit", "marketing", "--channel", "stable"}, 2, "shares the project's channel (beta)"},
		{[]string{"kit", "add", "demo", "--kit", "marketing", "--targets", "codex"}, 2, "drop --channel and --targets"},
		{[]string{"kit", "remove", "demo"}, 2, "pass --kit NAME"},
		{[]string{"kit", "remove", "demo", "--kit", "engineer"}, 1, "is the primary kit of demo"},
		{[]string{"kit", "remove", "demo", "--kit", "marketing"}, 1, "does not use AgentKit marketing"},
	} {
		if r := acp(t, f, "", c.args...); r.code != c.code || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%v: exit %d %q", c.args, r.code, r.stderr)
		}
	}

	r := mustOK(t, acp(t, f, "", "kit", "add", "demo", "--kit", "marketing"))
	if !strings.Contains(r.stdout, "AgentKit marketing claude-code: not installed -> 3.0.0-beta.36") ||
		!strings.Contains(r.stdout, "refresh 3.0.0-beta.36 again so its shared agents win") {
		t.Fatalf("preview:\n%s", r.stdout)
	}
	if readMap(t, manifest)["agentkit"].(map[string]any)["extra_kits"] != nil {
		t.Fatal("the preview wrote the manifest")
	}
	mustOK(t, acp(t, f, "", "kit", "add", "demo", "--kit", "marketing", "--apply"))
	extras := readMap(t, manifest)["agentkit"].(map[string]any)["extra_kits"].([]any)
	if len(extras) != 1 || extras[0].(map[string]any)["kit"] != "marketing" || extras[0].(map[string]any)["skills"] != "all" {
		t.Fatalf("extra_kits %v", extras)
	}
	if !f.Exists(filepath.Join(f.Repo, ".claude/skills/marketing-brief/SKILL.md")) {
		t.Fatal("kit add did not install marketing")
	}
	if r := acp(t, f, "", "kit", "add", "demo", "--kit", "marketing"); r.code != 1 || !strings.Contains(r.stderr, "already uses AgentKit marketing") {
		t.Fatalf("second add: %d %q", r.code, r.stderr)
	}

	code, data := acpJSON(t, f, "status")
	if code != 0 {
		t.Fatalf("status exit %d", code)
	}
	p := data["projects"].([]any)[0].(map[string]any)
	ak := p["agentkit"].(map[string]any)
	extra := ak["extra_kits"].([]any)[0].(map[string]any)
	if ak["kit"] != "engineer" || extra["kit"] != "marketing" || extra["pin"] != "3.0.0-beta.36" || p["healthy"] != true {
		t.Fatalf("status agentkit %v healthy %v notes %v", ak, p["healthy"], p["notes"])
	}
	for _, it := range extra["targets"].([]any) {
		if tg := it.(map[string]any); tg["ok"] != true {
			t.Errorf("marketing target %v", tg)
		}
	}

	_, data = acpJSON(t, f, "skills", "list", "demo")
	if got := data["agentkit"].(map[string]any)["extra_kits"].([]any)[0].(map[string]any); got["kit"] != "marketing" || got["mode"] != "all" {
		t.Fatalf("skills list extra %v", got)
	}

	_, data = acpJSON(t, f, "upgrade", "--skip-binary", "--no-sync", "--dry-run", "--version", "3.0.0-beta.36")
	if resolved := data["resolved"].(map[string]any); resolved["engineer"] == nil || resolved["marketing"] == nil {
		t.Fatalf("upgrade resolved %v", resolved)
	}

	os.Remove(filepath.Join(f.Repo, ".claude/skills/marketing-brief/SKILL.md"))
	_, data = acpJSON(t, f, "scan")
	var subjects []string
	for _, it := range data["findings"].([]any) {
		if fd := it.(map[string]any); fd["code"] == "kit_files_missing" {
			subjects = append(subjects, fd["subject"].(string))
		}
	}
	if strings.Join(subjects, ",") != "demo claude-code marketing" {
		t.Fatalf("kit_files_missing subjects %v", subjects)
	}
	mustOK(t, acp(t, f, "", "sync", "demo"))

	r = mustOK(t, acp(t, f, "", "kit", "remove", "demo", "--kit", "marketing"))
	if !strings.Contains(r.stdout, "- AgentKit marketing: ak kit uninstall") {
		t.Fatalf("remove preview:\n%s", r.stdout)
	}
	if readMap(t, manifest)["agentkit"].(map[string]any)["extra_kits"] == nil {
		t.Fatal("the preview wrote the manifest")
	}
	mustOK(t, acp(t, f, "", "kit", "remove", "demo", "--kit", "marketing", "--apply"))
	if _, ok := readMap(t, manifest)["agentkit"].(map[string]any)["extra_kits"]; ok {
		t.Fatal("extra_kits is still in the manifest")
	}
	if f.Exists(filepath.Join(f.Repo, ".claude/skills/marketing-brief")) || !f.Exists(filepath.Join(f.Repo, ".claude/agents/planner.md")) {
		t.Fatal("marketing is still installed, or engineer's shared file was not written back")
	}
	if code, data := acpJSON(t, f, "doctor"); code != 0 {
		t.Fatalf("doctor after remove: %d %v", code, data)
	}
}

// TestSkillsSetEditsTheKitThatOwnsTheSkill excludes a skill of an added kit in that kit's entry.
func TestSkillsSetEditsTheKitThatOwnsTheSkill(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.CP, "agentkit-releases.lock.json"),
		`{"schemaVersion": 2, "channels": {"beta": {"engineer": "3.0.0-beta.36", "marketing": "3.0.0-beta.36"}}}`)
	f.Manifest("demo", `{"id": "demo", "skills": [], "agentkit": {"kit": "engineer", "channel": "beta",
  "targets": ["claude-code"], "skills": "all", "extra_kits": [{"kit": "marketing", "skills": "all"}]}}`)
	f.Write(filepath.Join(f.Repo, ".agentkit/adapters/claude-code/marketing/.agentkit/install-manifest.json"),
		`{"provenance": {"registry_version": "3.0.0-beta.36"}, "skill_selection": {"mode": "list", "skills": ["marketing-brief", "mk-seo"]}}`)
	mustOK(t, acp(t, f, "", "skills", "set", "demo", "--remove", "mk-seo", "--apply"))
	ak := readMap(t, filepath.Join(f.CP, "projects/demo.json"))["agentkit"].(map[string]any)
	extra := ak["extra_kits"].([]any)[0].(map[string]any)
	if _, ok := ak["exclude"]; ok || strings.Join(toStrings(extra["exclude"]), ",") != "mk-seo" {
		t.Fatalf("agentkit %v", ak)
	}
}
