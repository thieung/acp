package commands

import (
	"bufio"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/thieung/acp/internal/testkit"
)

// TestMCPServesOnStdio talks to `acp mcp` like a client: stdin stays open until the answers arrive.
func TestMCPServesOnStdio(t *testing.T) {
	f := testkit.New(t)
	cmd := exec.Command(acpBin, "mcp")
	cmd.Dir = f.Base
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
`)
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var got strings.Builder
	deadline := time.After(20 * time.Second)
	for !strings.Contains(got.String(), `"id":2`) {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("acp mcp exited early:\n%s", got.String())
			}
			got.WriteString(l + "\n")
		case <-deadline:
			cmd.Process.Kill()
			t.Fatalf("no tools/list answer:\n%s", got.String())
		}
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("acp mcp: %v", err)
	}
	for _, want := range []string{`"name":"acp"`, `"acp_fix_preview"`, `"readOnlyHint":true`} {
		if !strings.Contains(got.String(), want) {
			t.Fatalf("%s missing from:\n%s", want, got.String())
		}
	}
}
