package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

// sessionStart returns the commands of every SessionStart hook in a settings file.
func sessionStart(t *testing.T, path string) []string {
	t.Helper()
	var out []string
	hooks, _ := readMap(t, path)["hooks"].(map[string]any)
	groups, _ := hooks["SessionStart"].([]any)
	for _, g := range groups {
		inner, _ := g.(map[string]any)["hooks"].([]any)
		for _, h := range inner {
			out = append(out, h.(map[string]any)["command"].(string))
		}
	}
	return out
}

func TestHooksInstallIsIdempotentAndReversible(t *testing.T) {
	f := testkit.New(t)
	bin := installableBinary(t, f, filepath.Join(f.Base, "bin"))
	settings := filepath.Join(f.Home, ".claude/settings.json")
	codex := filepath.Join(f.Home, ".codex/hooks.json")
	f.Write(settings, `{"model": "opus", "hooks": {}}`)
	f.Write(codex, `{"hooks": {"SessionStart": []}}`)
	mustOK(t, acp(t, f, "", "hooks", "install"))
	mustOK(t, acp(t, f, "", "hooks", "install"))
	want := bin + " link --hook"
	for _, path := range []string{settings, codex} {
		if got := sessionStart(t, path); len(got) != 1 || got[0] != want {
			t.Fatalf("%s: %q", path, got)
		}
	}
	if readMap(t, settings)["model"] != "opus" {
		t.Fatal("install dropped other settings")
	}
	r := mustOK(t, acp(t, f, "", "hooks"))
	if strings.Count(r.stdout, "installed") != 2 || strings.Contains(r.stdout, "not installed") {
		t.Fatalf("status:\n%s", r.stdout)
	}
	backups, _ := filepath.Glob(filepath.Join(f.CP, "runtime/backups/*"))
	if len(backups) != 2 {
		t.Fatalf("backups %v (one per changed file)", backups)
	}
	for _, b := range backups {
		if fi, _ := os.Stat(b); fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", b, fi.Mode().Perm())
		}
	}
	mustOK(t, acp(t, f, "", "hooks", "uninstall"))
	if got := sessionStart(t, settings); len(got) != 0 {
		t.Fatalf("after uninstall: %q", got)
	}
}

func TestHooksInstallReplacesOldFormsAndKeepsUserHooks(t *testing.T) {
	f := testkit.New(t)
	bin := installableBinary(t, f, filepath.Join(f.Base, "bin"))
	settings := filepath.Join(f.Home, ".claude/settings.json")
	f.Write(settings, `{"hooks": {"SessionStart": [
  {"matcher": "startup|resume", "hooks": [{"type": "command", "command": "python3 `+f.CP+`/scripts/acp link --hook"}]},
  {"matcher": "startup", "hooks": [{"type": "command", "command": "/tmp/x/acp link --hook"},
    {"type": "command", "command": "my-acp link --hook"}]}]}}`)
	os.Chmod(settings, 0o600)
	mustOK(t, acp(t, f, "", "hooks", "install"))
	got := sessionStart(t, settings)
	if len(got) != 2 || got[0] != "my-acp link --hook" || got[1] != bin+" link --hook" {
		t.Fatalf("after install: %q", got)
	}
	if fi, _ := os.Stat(settings); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600 kept", fi.Mode().Perm())
	}
	mustOK(t, acp(t, f, "", "hooks", "uninstall"))
	if got := sessionStart(t, settings); len(got) != 1 || got[0] != "my-acp link --hook" {
		t.Fatalf("uninstall touched a user hook: %q", got)
	}
}

func TestHooksWriteThroughSymlinkedCCSSettings(t *testing.T) {
	f := testkit.New(t)
	installableBinary(t, f, filepath.Join(f.Base, "bin"))
	shared := filepath.Join(f.Home, ".ccs/shared/settings.json")
	f.Write(shared, `{}`)
	for _, name := range []string{"work", "personal"} {
		f.Symlink(shared, filepath.Join(f.Home, ".ccs/instances", name, "settings.json"))
	}
	mustOK(t, acp(t, f, "", "hooks", "install"))
	for _, name := range []string{"work", "personal"} {
		if !f.IsLink(filepath.Join(f.Home, ".ccs/instances", name, "settings.json")) {
			t.Fatalf("%s settings is no longer a symlink", name)
		}
	}
	if got := sessionStart(t, shared); len(got) != 1 {
		t.Fatalf("shared CCS settings: %q", got)
	}
}

func TestHooksQuoteAPathWithASpaceAndTheHookRuns(t *testing.T) {
	f := testkit.New(t)
	bin := installableBinary(t, f, filepath.Join(f.Base, "my bin"))
	settings := filepath.Join(f.Home, ".claude/settings.json")
	f.Write(settings, `{}`)
	mustOK(t, acp(t, f, "", "hooks", "install", "--settings", filepath.Join(f.Base, "extra.json")))
	got := sessionStart(t, settings)
	if len(got) != 1 || got[0] != "'"+bin+"' link --hook" {
		t.Fatalf("command %q", got)
	}
	cmd := exec.Command("sh", "-c", got[0])
	cmd.Stdin = strings.NewReader(`{"cwd": "` + f.WT + `"}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook command: %v\n%s", err, out)
	}
	if !f.IsLink(filepath.Join(f.WT, ".claude/skills/herdr")) {
		t.Fatal("the installed hook did not link the worktree")
	}
}

func TestHooksRefuseATemporaryBinary(t *testing.T) {
	f := testkit.New(t)
	installableBinary(t, f, filepath.Join(f.Base, "bin"))
	t.Setenv("ACP_BIN", filepath.Join(f.Base, "tmp/go-run/acp"))
	f.Executable(filepath.Join(f.Base, "tmp/go-run/acp"), "#!/bin/sh\n")
	settings := filepath.Join(f.Home, ".claude/settings.json")
	f.Write(settings, `{}`)
	r := acp(t, f, "", "hooks", "install")
	if r.code != 1 || !strings.Contains(r.stderr, "temporary") {
		t.Fatalf("exit %d %q", r.code, r.stderr)
	}
	if f.Read(settings) != "{}" {
		t.Fatal("settings changed")
	}
}

func TestHooksDryRunWritesNothing(t *testing.T) {
	f := testkit.New(t)
	installableBinary(t, f, filepath.Join(f.Base, "bin"))
	settings := filepath.Join(f.Home, ".claude/settings.json")
	f.Write(settings, `{}`)
	r := mustOK(t, acp(t, f, "", "hooks", "install", "--dry-run"))
	if !strings.Contains(r.stdout, "would install") || f.Read(settings) != "{}" {
		t.Fatalf("dry run:\n%s", r.stdout)
	}
}

func TestHooksRefuseANonExecutableBinary(t *testing.T) {
	f := testkit.New(t)
	bin := installableBinary(t, f, filepath.Join(f.Base, "bin"))
	os.Chmod(bin, 0o644)
	f.Write(filepath.Join(f.Home, ".claude/settings.json"), `{}`)
	if r := acp(t, f, "", "hooks", "install"); r.code != 1 || !strings.Contains(r.stderr, "not an executable file") {
		t.Fatalf("exit %d %q", r.code, r.stderr)
	}
}

func TestHooksRecogniseTheirOwnCommandWhateverTheBinaryIsCalled(t *testing.T) {
	f := testkit.New(t)
	bin := installableBinary(t, f, filepath.Join(f.Base, "bin"))
	renamed := filepath.Join(f.Base, "bin", "acp-go")
	if err := os.Rename(bin, renamed); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACP_BIN", renamed)
	settings := filepath.Join(f.Home, ".claude/settings.json")
	f.Write(settings, `{}`)
	mustOK(t, acp(t, f, "", "hooks", "install"))
	mustOK(t, acp(t, f, "", "hooks", "install"))
	if got := sessionStart(t, settings); len(got) != 1 || got[0] != renamed+" link --hook" {
		t.Fatalf("after two installs: %q", got)
	}
	if r := mustOK(t, acp(t, f, "", "hooks")); !strings.Contains(r.stdout, "claude: installed") {
		t.Fatalf("status:\n%s", r.stdout)
	}
	mustOK(t, acp(t, f, "", "hooks", "uninstall"))
	if got := sessionStart(t, settings); len(got) != 0 {
		t.Fatalf("after uninstall: %q", got)
	}
	// Later runs without ACP_BIN still know the hook: scan does not call it missing, install
	// replaces it instead of adding a second one, and uninstall removes it.
	mustOK(t, acp(t, f, "", "hooks", "install"))
	t.Setenv("ACP_BIN", "")
	if r := acp(t, f, "", "scan", "--json"); strings.Contains(r.stdout, "acp_hook_missing") {
		t.Fatalf("scan calls the renamed hook missing:\n%s", r.stdout)
	}
	mustOK(t, acp(t, f, "", "hooks", "install"))
	if got := sessionStart(t, settings); len(got) != 1 {
		t.Fatalf("install without ACP_BIN added a second hook: %q", got)
	}
	t.Setenv("ACP_BIN", renamed)
	mustOK(t, acp(t, f, "", "hooks", "install"))
	t.Setenv("ACP_BIN", "")
	mustOK(t, acp(t, f, "", "hooks", "uninstall"))
	if got := sessionStart(t, settings); len(got) != 0 {
		t.Fatalf("after uninstall: %q", got)
	}
}
