package commands

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thieung/acp/internal/testkit"
)

func TestWebServesUntilStopped(t *testing.T) {
	f := testkit.New(t)
	cmd := exec.Command(acpBin, "web", "--port", "0", "--no-open", "--print-url")
	cmd.Dir = f.Base
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	urls := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if m := regexp.MustCompile(`http://127\.0\.0\.1:\d+/#code=\w+`).FindString(sc.Text()); m != "" {
				urls <- m
			}
		}
	}()
	var u string
	select {
	case u = <-urls:
	case <-time.After(20 * time.Second):
		t.Fatal("acp web printed no sign-in URL")
	}
	base, code, _ := strings.Cut(u, "/#code=")
	resp, err := http.Post(base+"/api/session", "application/json", strings.NewReader(`{"code":"`+code+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&s)
	resp.Body.Close()
	req, _ := http.NewRequest("GET", base+"/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("status: %v %v", err, resp)
	}
	resp.Body.Close()
	cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acp web did not stop cleanly: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("acp web ignored SIGTERM")
	}
}

func TestWebRefusesABusyPort(t *testing.T) {
	f := testkit.New(t)
	cmd := exec.Command(acpBin, "web", "--port", "0", "--no-open", "--print-url")
	cmd.Dir = f.Base
	stdout, _ := cmd.StdoutPipe()
	cmd.Start()
	defer cmd.Process.Kill()
	sc := bufio.NewScanner(stdout)
	var port string
	for sc.Scan() {
		if m := regexp.MustCompile(`127\.0\.0\.1:(\d+)`).FindStringSubmatch(sc.Text()); m != nil {
			port = m[1]
			break
		}
	}
	r := acp(t, f, "", "web", "--port", port, "--no-open")
	if r.code != 1 || !strings.Contains(r.stderr, "already in use") {
		t.Fatalf("busy port: %d %q", r.code, r.stderr)
	}
}
