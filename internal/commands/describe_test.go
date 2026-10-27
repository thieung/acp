package commands

import (
	"strings"
	"testing"
)

func TestDescribeContract(t *testing.T) {
	code, stdout, _ := run(t, "describe", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	env := envelope(t, stdout)
	if env["kind"] != "acp.describe" {
		t.Fatalf("kind %v", env["kind"])
	}
	data := env["data"].(map[string]any)
	if data["schema"] != "acp.describe/2" {
		t.Fatalf("schema %v", data["schema"])
	}
	names := map[string]bool{}
	for _, c := range data["commands"].([]any) {
		cmd := c.(map[string]any)
		name := cmd["name"].(string)
		names[name] = true
		if _, ok := cmd["agent"]; ok {
			t.Errorf("%s still has an agent field", name)
		}
		for k, v := range cmd {
			if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), "fix") {
				t.Errorf("%s.%s mentions fix: %q", name, k, s)
			}
		}
		if cmd["mutation"] == true && cmd["preview"] == nil {
			t.Errorf("mutation %s has no preview", name)
		}
	}
	for _, want := range []string{"status", "doctor", "sync", "link", "unlink", "upgrade", "add", "remove",
		"activity", "global", "hooks", "skills list", "skills set", "kit add", "setup", "setup install-ak",
		"setup login", "describe", "web", "init", "scan", "explain", "mcp"} {
		if !names[want] {
			t.Errorf("describe is missing %q", want)
		}
	}
	for name := range names {
		if strings.HasPrefix(name, "fix") {
			t.Errorf("describe still lists %q", name)
		}
	}
	// Every described command exists in the parser and vice versa.
	for name := range names {
		if _, err := parse(append(strings.Fields(name), "-h")); err != errHelp {
			t.Errorf("%q is described but does not parse: %v", name, err)
		}
	}
}

func TestDescribeOne(t *testing.T) {
	code, stdout, _ := run(t, "describe", "skills", "set")
	if code != 0 || !strings.HasPrefix(stdout, "skills set") || !strings.Contains(stdout, "--apply") {
		t.Fatalf("%d %q", code, stdout)
	}
	code, _, stderr := run(t, "describe", "fix", "run")
	if code != 1 || !strings.Contains(stderr, "no command 'fix run'") {
		t.Fatalf("%d %q", code, stderr)
	}
}
