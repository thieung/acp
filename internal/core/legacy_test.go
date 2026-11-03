package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHookCommandRecognition(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/Users/x/.local/bin/acp link --hook":                          true,
		"'/Users/x/my bin/acp' link --hook":                            true,
		"acp link --hook":                                              true,
		"python3 /Users/x/agent-control-plane/scripts/acp link --hook": true,
		"my-acp link --hook":                                           false,
		"/usr/bin/acp link --hook --verbose":                           false,
		"/usr/bin/acp link":                                            false,
		"echo /usr/bin/acp link --hook":                                false,
		"python3 /x/scripts/acp-old link --hook":                       false,
	} {
		if got := IsAcpHook(cmd); got != want {
			t.Errorf("IsAcpHook(%q) = %v, want %v", cmd, got, want)
		}
	}
	if got := HookBinary("'/a b/acp' link --hook"); got != "/a b/acp" {
		t.Fatalf("HookBinary %q", got)
	}
}

func TestHookBinaryKnowsARenamedAcpByItsMarker(t *testing.T) {
	dir := t.TempDir()
	renamed, script := filepath.Join(dir, "acp-go"), filepath.Join(dir, "my-acp")
	os.WriteFile(renamed, []byte("\x7fELF...."+BinaryMarker+"...."), 0o755)
	os.WriteFile(script, []byte("#!/bin/sh\necho mine\n"), 0o755)
	if got := HookBinary(renamed + " link --hook"); got != renamed {
		t.Fatalf("renamed acp not recognised: %q", got)
	}
	if got := HookBinary(script + " link --hook"); got != "" {
		t.Fatalf("a user's own script was taken for acp: %q", got)
	}
	if got := HookBinary(filepath.Join(dir, "gone") + " link --hook"); got != "" {
		t.Fatalf("a missing binary not named acp was taken for acp: %q", got)
	}
}
