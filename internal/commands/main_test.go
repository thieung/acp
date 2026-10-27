package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

// acpBin is the acp binary built once for the integration tests, which run it like a user would
// (separate processes, so locks behave as on a real machine).
var acpBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "acp-test-bin")
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

type result struct {
	code           int
	stdout, stderr string
}

// acp runs the binary from the fixture's base folder (outside every repo) with stdin.
func acp(t *testing.T, f *testkit.Fixture, stdin string, args ...string) result {
	t.Helper()
	return acpIn(t, f.Base, stdin, args...)
}

func acpIn(t *testing.T, dir, stdin string, args ...string) result {
	t.Helper()
	cmd := exec.Command(acpBin, args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewBufferString(stdin)
	var out, errw bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errw
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result{code, out.String(), errw.String()}
}

func mustOK(t *testing.T, r result) result {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	return r
}

// addProject registers a second repo "other" with one worktree.
func addProject(t *testing.T, f *testkit.Fixture, id string) (repo, wt string) {
	repo, wt = filepath.Join(f.Base, id), filepath.Join(f.Base, id+"-wt")
	os.MkdirAll(repo, 0o755)
	f.Git(repo, "init", "-q")
	f.Write(filepath.Join(repo, "README.md"), id)
	f.Git(repo, "add", ".")
	f.Git(repo, "commit", "-qm", "init")
	f.Git(repo, "worktree", "add", "-q", "-b", "feature", wt)
	f.Manifest(id, `{"id": "`+id+`", "skills": [{"name": "herdr", "runtimes": ["claude"]}],
  "agentkit": {"kit": "engineer", "channel": "beta", "targets": ["claude-code"], "skills": "all"}}`)
	f.Overlay(map[string]string{"demo": f.Repo, id: repo})
	return repo, wt
}

// acpJSON runs a command with --json and returns the exit code and the envelope's data.
func acpJSON(t *testing.T, f *testkit.Fixture, args ...string) (int, map[string]any) {
	t.Helper()
	r := acp(t, f, "", append(args, "--json")...)
	env := envelope(t, r.stdout)
	data, _ := env["data"].(map[string]any)
	if msg, ok := env["error"].(string); ok {
		data["error"] = msg
	}
	return r.code, data
}

func readMap(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

// installableBinary copies acp outside the temp folder the child sees, so `hooks install` accepts
// it: the child's TMPDIR is moved into the fixture.
func installableBinary(t *testing.T, f *testkit.Fixture, dir string) string {
	t.Helper()
	t.Setenv("TMPDIR", filepath.Join(f.Base, "tmp"))
	os.MkdirAll(filepath.Join(f.Base, "tmp"), 0o755)
	data, err := os.ReadFile(acpBin)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "acp")
	f.Write(path, "")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0o755)
	t.Setenv("ACP_BIN", path)
	return path
}
