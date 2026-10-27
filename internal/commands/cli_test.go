package commands

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := Main(args, strings.NewReader(""), &out, &errw)
	return code, out.String(), errw.String()
}

func envelope(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("not a JSON envelope: %v\n%s", err, stdout)
	}
	return env
}

func TestParseFlagsBeforeOrAfterPositionals(t *testing.T) {
	a, err := parse([]string{"sync", "demo", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := parse([]string{"sync", "--dry-run", "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.lists, b.lists) || !reflect.DeepEqual(a.bools, b.bools) {
		t.Fatalf("%+v != %+v", a, b)
	}
	if !a.Bool("dry-run") || strings.Join(a.List("ids"), ",") != "demo" {
		t.Fatalf("parsed %+v", a)
	}
	c, err := parse([]string{"--json", "activity", "--days=7", "--min", "1"})
	if err != nil || !c.json || c.Int("days") != 7 || c.Int("min") != 1 {
		t.Fatalf("%+v %v", c, err)
	}
	d, err := parse([]string{"hooks", "install", "--settings", "a.json", "--settings=b.json"})
	if err != nil || strings.Join(d.List("settings"), ",") != "a.json,b.json" || d.Str("action") != "install" {
		t.Fatalf("%+v %v", d, err)
	}
	e, err := parse([]string{"skills", "set", "demo", "--add", "x,y"})
	if err != nil || e.Str("id") != "demo" || e.Str("add") != "x,y" || e.Kind() != "acp.skills.set" {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestBareAcpIsStatus(t *testing.T) {
	a, err := parse([]string{"--json"})
	if err != nil || a.cmd.name != "status" || !a.json || a.Kind() != "acp.status" {
		t.Fatalf("%+v %v", a, err)
	}
	h, _ := parse([]string{"hooks"})
	s, _ := parse([]string{"setup"})
	if h.Kind() != "acp.hooks.status" || s.Kind() != "acp.setup.check" {
		t.Fatalf("default action kinds: %s %s", h.Kind(), s.Kind())
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"sync", "--bogus"},
		{"nope"},
		{"remove"},
		{"upgrade", "--channel", "nightly"},
		{"activity", "--days", "x"},
		{"skills", "list", "a", "b"},
	} {
		code, _, stderr := run(t, args...)
		if code != 2 || !strings.Contains(stderr, "usage: acp") {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
	code, stdout, _ := run(t, "sync", "--bogus", "--json")
	env := envelope(t, stdout)
	if code != 2 || env["exit_code"].(float64) != 2 || !strings.Contains(env["error"].(string), "--bogus") || env["kind"] != "acp.sync" {
		t.Fatalf("exit %d envelope %v", code, env)
	}
}

func TestNoFixIsAcceptedAsANoOp(t *testing.T) {
	for _, cmd := range [][]string{{"sync", "--all"}, {"upgrade"}, {"add"}, {"skills", "set", "x"}, {"kit", "add", "x"}} {
		a, err := parse(append(cmd, "--no-fix"))
		if err != nil || !a.Bool("no-fix") {
			t.Errorf("%v: %v", cmd, err)
		}
	}
	if _, err := parse([]string{"link", "--no-fix"}); err == nil {
		t.Error("link never took --no-fix")
	}
	var errw bytes.Buffer
	warnNoFix(&errw, true)
	if !strings.Contains(errw.String(), "--no-fix") {
		t.Fatalf("no notice: %q", errw.String())
	}
}

func TestErrorEnvelopeForUnknownProject(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "projects"), 0o755)
	os.WriteFile(filepath.Join(root, "agentkit-releases.lock.json"), []byte(`{"schemaVersion":2,"channels":{}}`), 0o644)
	t.Setenv("ACP_ROOT", root)
	failing := func(e *Env, a *Args) (int, error) {
		e.Printf("looking\n")
		_, err := e.Project("nope")
		return 0, err
	}
	var out, errw bytes.Buffer
	a, _ := parse([]string{"skills", "list", "nope", "--json"})
	code := execute(a, failing, strings.NewReader(""), &out, &errw)
	env := envelope(t, out.String())
	if code != 1 || env["exit_code"].(float64) != 1 {
		t.Fatalf("exit %d: %v", code, env)
	}
	if env["error"] != "no project 'nope' (see `acp status`)" || env["kind"] != "acp.skills.list" {
		t.Fatalf("envelope %v", env)
	}
	if env["data"].(map[string]any)["text"] != "looking\n" {
		t.Fatalf("data.text: %v", env["data"])
	}
	out.Reset()
	a, _ = parse([]string{"skills", "list", "nope"})
	code = execute(a, failing, strings.NewReader(""), &out, &errw)
	if code != 1 || strings.TrimSpace(errw.String()) != "acp: no project 'nope' (see `acp status`)" || out.String() != "looking\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, out.String(), errw.String())
	}
}

func TestVersionAndHelp(t *testing.T) {
	code, stdout, _ := run(t, "--version")
	if code != 0 || !strings.HasPrefix(stdout, "acp ") {
		t.Fatalf("%d %q", code, stdout)
	}
	code, stdout, _ = run(t, "sync", "-h")
	if code != 0 || !strings.Contains(stdout, "--dry-run") || !strings.Contains(stdout, "--offline") {
		t.Fatalf("%d %q", code, stdout)
	}
}
