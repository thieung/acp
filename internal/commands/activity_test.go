package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thieung/acp/internal/testkit"
)

func TestActivityCountsSessionsPerMainCheckout(t *testing.T) {
	f := testkit.New(t)
	app := filepath.Join(f.Home, "code/app")
	os.MkdirAll(app, 0o755)
	f.Git(app, "init", "-q")
	// A temp folder outside HOME that is not in the fleet is noise; the fleet repo is in temp too.
	scratch := filepath.Join(f.Base, "scratch")
	os.MkdirAll(scratch, 0o755)
	claude := filepath.Join(f.Home, ".claude/projects")
	for i, cwd := range []string{app, app, f.WT, scratch, f.Home} {
		f.Write(filepath.Join(claude, "p", "s"+string(rune('a'+i))+".jsonl"),
			`{"type":"summary"}`+"\n"+`{"cwd":"`+cwd+`","type":"user"}`+"\n")
	}
	codex := filepath.Join(f.Home, ".codex/sessions/2026/10/27")
	f.Write(filepath.Join(codex, "rollout.jsonl"), `{"type":"session_meta","payload":{"cwd":"`+f.Repo+`"}}`+"\n")
	old := filepath.Join(claude, "p", "old.jsonl")
	f.Write(old, `{"cwd":"`+app+`"}`+"\n")
	stale := time.Now().Add(-40 * 24 * time.Hour)
	os.Chtimes(old, stale, stale)

	code, data := acpJSON(t, f, "activity", "--min", "1")
	if code != 0 {
		t.Fatalf("exit %d: %v", code, data)
	}
	rows := map[string]map[string]any{}
	for _, it := range data["repos"].([]any) {
		row := it.(map[string]any)
		rows[row["repo"].(string)] = row
	}
	if len(rows) != 2 {
		t.Fatalf("rows %v; scratch, HOME and old sessions are skipped", rows)
	}
	if r := rows[app]; r["claude"] != 2.0 || r["codex"] != 0.0 || r["project"] != nil {
		t.Fatalf("app row %v", r)
	}
	if r := rows[f.Repo]; r["claude"] != 1.0 || r["codex"] != 1.0 || r["project"] != "demo" {
		t.Fatalf("demo row %v (the worktree session counts for its main checkout)", r)
	}
	if text := data["text"].(string); !strings.Contains(text, "demo") {
		t.Fatalf("text:\n%s", text)
	}
	_, data = acpJSON(t, f, "activity")
	if len(data["repos"].([]any)) != 2 {
		t.Fatalf("--min 2 default: %v", data["repos"])
	}
}

func TestActivityNamesIdleFleetProjects(t *testing.T) {
	f := testkit.New(t)
	code, data := acpJSON(t, f, "activity")
	if code != 0 || len(data["idle"].([]any)) != 1 || data["idle"].([]any)[0] != "demo" {
		t.Fatalf("exit %d idle %v", code, data["idle"])
	}
}
