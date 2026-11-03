package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/testkit"
)

// snapshot maps every file under dir to its content.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

func TestInitCreatesAControlPlaneInAnEmptyDir(t *testing.T) {
	f := testkit.New(t)
	t.Setenv("ACP_ROOT", "")
	root := filepath.Join(f.Base, "fresh")
	r := mustOK(t, acp(t, f, "", "init", "--root", root))
	for _, name := range []string{"projects.local.json", "agentkit-releases.lock.json", "global-skills.json", ".gitignore", "AGENTS.md"} {
		if !f.Exists(filepath.Join(root, name)) {
			t.Errorf("%s missing", name)
		}
	}
	for _, dir := range []string{"projects", "skills", "runtime"} {
		if !f.IsDir(filepath.Join(root, dir)) {
			t.Errorf("%s/ missing", dir)
		}
	}
	if e, _ := os.ReadDir(filepath.Join(root, "skills")); len(e) != 0 {
		t.Fatal("init ships personal skills")
	}
	if lock := readMap(t, filepath.Join(root, "agentkit-releases.lock.json")); lock["schemaVersion"] != 2.0 {
		t.Fatalf("lock %v", lock)
	}
	gi := f.Read(filepath.Join(root, ".gitignore"))
	if !strings.Contains(gi, "projects.local.json") || !strings.Contains(gi, "runtime/") {
		t.Fatalf(".gitignore:\n%s", gi)
	}
	if !strings.Contains(r.stdout, "acp add") || !strings.Contains(r.stdout, "acp scan") {
		t.Fatalf("next steps missing:\n%s", r.stdout)
	}
	if got := strings.TrimSpace(f.Read(core.ConfigFile())); got != root {
		t.Fatalf("config file names %q", got)
	}
	// The new root is found without ACP_ROOT.
	if r := acp(t, f, "", "status", "--json"); r.code != 0 || !strings.Contains(r.stdout, `"projects": []`) {
		t.Fatalf("status in the new root: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestInitOnAnExistingControlPlaneChangesNothing(t *testing.T) {
	f := testkit.New(t)
	t.Setenv("ACP_ROOT", "")
	f.Write(filepath.Join(f.CP, ".gitignore"), "projects.local.json\nruntime/\n")
	f.Write(filepath.Join(f.CP, "AGENTS.md"), "my own rules\n")
	f.Write(filepath.Join(f.CP, "global-skills.json"), `{"version": 1, "skills": []}`)
	os.MkdirAll(filepath.Join(f.CP, "runtime"), 0o755)
	before := snapshot(t, f.CP)
	r := mustOK(t, acp(t, f, "", "init", "--root", f.CP))
	if !reflectEqual(before, snapshot(t, f.CP)) {
		t.Fatal("init changed a file in an existing control plane")
	}
	if !strings.Contains(r.stdout, "kept") {
		t.Fatalf("stdout:\n%s", r.stdout)
	}
}

func TestInitDryRunAndTheConfigFileFollowsTheLastExplicitRoot(t *testing.T) {
	f := testkit.New(t)
	t.Setenv("ACP_ROOT", "")
	other := filepath.Join(f.Base, "other-root")
	mustOK(t, acp(t, f, "", "init", "--root", other, "--dry-run"))
	if f.Exists(other) || f.Exists(core.ConfigFile()) {
		t.Fatal("dry run wrote")
	}
	mustOK(t, acp(t, f, "", "init", "--root", other))
	if got := strings.TrimSpace(f.Read(core.ConfigFile())); got != other {
		t.Fatalf("config file names %q after init --root", got)
	}
	def := filepath.Join(f.Home, "agent-control-plane")
	mustOK(t, acp(t, f, "", "init", "--root", def))
	if got := strings.TrimSpace(f.Read(core.ConfigFile())); got != def {
		t.Fatalf("config file names %q after the last init", got)
	}
	if r := acp(t, f, "", "init", "--root", filepath.Join(f.CP, "projects/demo.json")); r.code == 0 {
		t.Fatal("init into a file was accepted")
	}
}

func reflectEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestInitWithoutRootKeepsTheRecordedControlPlane(t *testing.T) {
	f := testkit.New(t)
	t.Setenv("ACP_ROOT", "")
	mine := filepath.Join(f.Base, "mine")
	mustOK(t, acp(t, f, "", "init", "--root", mine))
	conf := core.ConfigFile()
	before, _ := os.ReadFile(conf)
	// The installer runs a bare `acp init` on every install, upgrades included.
	mustOK(t, acp(t, f, "", "init"))
	after, _ := os.ReadFile(conf)
	if string(after) != string(before) {
		t.Fatalf("config moved: %q -> %q", before, after)
	}
	if f.Exists(filepath.Join(f.Home, "agent-control-plane")) {
		t.Fatal("a second, empty control plane was created")
	}
	t.Setenv("ACP_ROOT", f.CP)
	if r := mustOK(t, acp(t, f, "", "init", "--json")); !strings.Contains(r.stdout, f.CP) ||
		!strings.Contains(r.stdout, `"recorded": false`) {
		t.Fatalf("ACP_ROOT not used, or recorded: %s", r.stdout)
	}
	// ACP_ROOT is an override for one shell: init with it, even with --root, leaves the record alone.
	other := filepath.Join(f.Base, "throwaway")
	r := mustOK(t, acp(t, f, "", "init", "--root", other))
	if !strings.Contains(r.stdout, "not recorded") || !f.Exists(filepath.Join(other, "projects")) {
		t.Fatalf("init with ACP_ROOT and --root: %s", r.stdout)
	}
	if got, _ := os.ReadFile(conf); string(got) != string(before) {
		t.Fatalf("ACP_ROOT moved the record: %q -> %q", before, got)
	}
}
