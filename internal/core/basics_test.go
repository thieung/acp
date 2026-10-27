package core

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"Authorization: Bearer abc.def-123":               "Authorization: Bearer [redacted]",
		"curl -H 'authorization: bearer xyz'":             "curl -H 'authorization: bearer [redacted]'",
		"OPENAI_API_KEY=sk-live password: pw1":            "OPENAI_API_KEY=[redacted] password: [redacted]",
		`{"GITHUB_TOKEN":"ghp_abcdefghijklmnopqrstuvwx"}`: `{"GITHUB_TOKEN":"[redacted]"}`,
		"key sk-ABCDEFGHIJKLMNOPQRSTU here":               "key [redacted] here",
		"api_key=hunter2 rest":                            "api_key=[redacted] rest",
		"nothing secret":                                  "nothing secret",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	for _, leak := range []string{"abc.def-123", "xyz", "ghp_abcdefghijklmnopqrstuvwx", "hunter2", "sk-live", "pw1"} {
		for in := range cases {
			if strings.Contains(Redact(in), leak) {
				t.Errorf("%q leaks through", leak)
			}
		}
	}
}

func TestRedactValueWalksDecodedJSON(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`{"text": "run with password=\"hunter2\" now", "has_token": true,
		"nested": {"x": "ghp_abcdefghijklmnopqrstuvwx"}, "list": [{"t": "token=abcdef123"}]}`), &v)
	out, err := json.Marshal(RedactValue(v))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"hunter2", "ghp_abcdefghijklmnopqrstuvwx", "abcdef123"} {
		if strings.Contains(string(out), leak) {
			t.Errorf("%q leaks through: %s", leak, out)
		}
	}
	if !strings.Contains(string(out), `"has_token":true`) {
		t.Errorf("a non-string value changed: %s", out)
	}
}

func TestRedactAgentKitKeys(t *testing.T) {
	for _, key := range []string{"ak_live_FAKEKEY0000000000", "ak_test_FAKE-KEY_000", "ak_license_FAKEKEY0000000000"} {
		in := "ak said: invalid key " + key + " (try again)"
		if got := Redact(in); strings.Contains(got, key) || !strings.Contains(got, redacted) {
			t.Errorf("Redact(%q) = %q", in, got)
		}
	}
	if got := Redact("ak_kit_engineer and ak_live_short"); got != "ak_kit_engineer and ak_live_short" {
		t.Errorf("non-keys changed: %q", got)
	}
}

func TestValidNameAndSafeJoin(t *testing.T) {
	for _, ok := range []string{"demo", "a.b-c_1", "ak-cook"} {
		if !ValidName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "../x", "A", "-x", "a/b", strings.Repeat("a", 90)} {
		if ValidName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
	if _, err := SafeJoin("/r/projects", "../x.json"); err == nil {
		t.Error("SafeJoin must refuse an escaping name")
	}
	if p, err := SafeJoin("/r/projects", "demo.json"); err != nil || p != "/r/projects/demo.json" {
		t.Errorf("SafeJoin = %q, %v", p, err)
	}
}

// TestHelperHoldLock is not a test: TestLockScopes runs the test binary with it to hold a lock
// from another process, because flock locks only conflict across open files.
func TestHelperHoldLock(t *testing.T) {
	path := os.Getenv("ACP_TEST_HOLD_LOCK")
	if path == "" {
		t.Skip("helper process only")
	}
	l, err := Acquire(context.Background(), path)
	if err != nil {
		os.Exit(3)
	}
	defer l.Release()
	os.Stdout.WriteString("held\n")
	time.Sleep(10 * time.Second)
}

func TestLockScopes(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.lock"), filepath.Join(dir, "b.lock")
	holder := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLock$")
	holder.Env = append(os.Environ(), "ACP_TEST_HOLD_LOCK="+a)
	out, _ := holder.StdoutPipe()
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { holder.Process.Kill(); holder.Wait() }()
	line, _ := bufio.NewReader(out).ReadString('\n')
	if !strings.HasPrefix(line, "held") {
		t.Fatalf("helper did not take the lock: %q", line)
	}
	if _, err := AcquireWithin(a, 150*time.Millisecond); err == nil {
		t.Fatal("acquired a lock another process holds")
	}
	lb, err := AcquireWithin(b, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("a different lock must be free: %v", err)
	}
	// Re-entrant inside one process: a nested acquire of a held lock does not block.
	again, err := AcquireWithin(b, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("nested acquire: %v", err)
	}
	again.Release()
	lb.Release()
	// After the last release another process could take it; in-process it is free again.
	if l, err := AcquireWithin(b, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	} else {
		l.Release()
	}
}

func TestExecKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	// The grandchild keeps stdout open; without the group kill, Wait would hang for 30 s.
	script := "(sleep 30; echo late) & echo $! > '" + pidFile + "'; sleep 30"
	start := time.Now()
	res := Exec(context.Background(), ExecOpts{Timeout: 700 * time.Millisecond}, "sh", "-c", script)
	if !res.TimedOut {
		t.Fatalf("expected a timeout, got %+v", res)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Exec waited %v for a grandchild holding stdout", time.Since(start))
	}
	data, _ := os.ReadFile(pidFile)
	var pid int
	for _, c := range strings.TrimSpace(string(data)) {
		pid = pid*10 + int(c-'0')
	}
	if pid == 0 {
		t.Fatalf("the script did not record its child: %+v", res)
	}
	for i := 0; syscall.Kill(pid, 0) == nil; i++ {
		if i > 20 {
			t.Fatalf("grandchild %d is still alive", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestExecReportsExitAndOutput(t *testing.T) {
	res := Exec(context.Background(), ExecOpts{}, "sh", "-c", "echo out; echo err >&2; exit 3")
	if res.Code != 3 || strings.TrimSpace(res.Stdout) != "out" || strings.TrimSpace(res.Stderr) != "err" {
		t.Fatalf("%+v", res)
	}
	if res := Exec(context.Background(), ExecOpts{}, filepath.Join(t.TempDir(), "missing")); res.Err == nil {
		t.Fatal("a missing binary must return an error")
	}
}

func TestSelfRunnerPassesStdin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "cat-stdin")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := SelfRunner(bin, dir)
	out, err := run(context.Background(), []byte("ak_live_FAKEKEY0000000000"), "setup")
	if err != nil || string(out) != "ak_live_FAKEKEY0000000000" {
		t.Fatalf("stdin: %q %v", out, err)
	}
	if out, err := run(context.Background(), nil, "setup"); err != nil || len(out) != 0 {
		t.Fatalf("nil stdin must give the child no input: %q %v", out, err)
	}
}

func TestResolveRoot(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	mk := func(dir string) string {
		os.MkdirAll(filepath.Join(dir, "projects"), 0o755)
		os.WriteFile(filepath.Join(dir, "agentkit-releases.lock.json"), []byte("{}"), 0o644)
		return dir
	}
	def := mk(filepath.Join(home, "agent-control-plane"))
	conf := mk(filepath.Join(base, "configured"))
	checkout := mk(filepath.Join(base, "checkout"))
	exec.Command("git", "init", "-q", checkout).Run()
	os.MkdirAll(filepath.Join(checkout, "sub"), 0o755)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("ACP_ROOT", "")

	r, err := ResolveRoot(base)
	if err != nil || r.Dir != resolvePath(def) {
		t.Fatalf("default: %v %v", r.Dir, err)
	}
	os.MkdirAll(filepath.Join(home, ".config/acp"), 0o755)
	os.WriteFile(filepath.Join(home, ".config/acp/root"), []byte(conf+"\n"), 0o644)
	if r, _ := ResolveRoot(base); r.Dir != resolvePath(conf) {
		t.Fatalf("config file: %v", r.Dir)
	}
	// A control-plane worktree has no machine overlay, so its fleet would be empty.
	if r, _ := ResolveRoot(filepath.Join(checkout, "sub")); r.Dir != resolvePath(conf) {
		t.Fatalf("a checkout without projects.local.json should not win: %v", r.Dir)
	}
	os.WriteFile(filepath.Join(checkout, "projects.local.json"), []byte(`{"schemaVersion": 1, "repos": {}}`), 0o644)
	if r, _ := ResolveRoot(filepath.Join(checkout, "sub")); r.Dir != resolvePath(checkout) {
		t.Fatalf("enclosing checkout should win: %v", r.Dir)
	}
	t.Setenv("ACP_ROOT", def)
	if r, _ := ResolveRoot(filepath.Join(checkout, "sub")); r.Dir != resolvePath(def) {
		t.Fatalf("ACP_ROOT should win: %v", r.Dir)
	}
	t.Setenv("ACP_ROOT", "")
	os.Remove(filepath.Join(home, ".config/acp/root"))
	os.RemoveAll(def)
	if _, err := ResolveRoot(base); err == nil || !strings.Contains(err.Error(), "ACP_ROOT") {
		t.Fatalf("no root: %v", err)
	}
}

func TestExecDeadlineFromContextReadsWell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res := Exec(ctx, ExecOpts{}, "sleep", "5")
	if !res.TimedOut || res.Err == nil || strings.Contains(res.Err.Error(), "after 0s") ||
		!strings.Contains(res.Err.Error(), "timed out") {
		t.Fatalf("a deadline set by the caller: %+v", res)
	}
}
