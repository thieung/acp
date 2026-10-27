package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/testkit"
)

// acpBin is the acp binary the server runs, as it does in production.
var acpBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "acp-web-bin")
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

type client struct {
	t     *testing.T
	base  string
	token string
	srv   *Server
}

func start(t *testing.T, f *testkit.Fixture, run core.Runner) *client {
	t.Helper()
	if run == nil {
		run = core.SelfRunner(acpBin, f.Base)
	}
	s := New(&core.Root{Dir: f.CP}, run)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	s.SetAddr(u.Host)
	return &client{t: t, base: ts.URL, srv: s}
}

func (c *client) do(method, path string, body any, hdr map[string]string) (int, []byte, http.Header) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out, resp.Header
}

func (c *client) signIn() {
	c.t.Helper()
	code, body, _ := c.do("POST", "/api/session", map[string]string{"code": c.srv.Code()}, nil)
	var r struct{ Token string }
	if code != 200 || json.Unmarshal(body, &r) != nil || r.Token == "" {
		c.t.Fatalf("session: %d %s", code, body)
	}
	c.token = r.Token
}

type op struct {
	ID, Kind, State, Digest string
	Envelope                map[string]any
}

// run posts a kind and waits for its op to finish.
func (c *client) run(kind string, args map[string]any, digest string) (int, op) {
	c.t.Helper()
	body := map[string]any{"kind": kind, "args": args}
	if digest != "" {
		body["digest"] = digest
	}
	code, out, _ := c.do("POST", "/api/run", body, nil)
	if code != http.StatusAccepted {
		var o op
		json.Unmarshal(out, &o)
		return code, o
	}
	var started struct{ Op string }
	json.Unmarshal(out, &started)
	return code, c.wait(started.Op)
}

// wait polls an op until it is done.
func (c *client) wait(id string) op {
	c.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		_, out, _ := c.do("GET", "/api/ops/"+id, nil, nil)
		var o op
		if err := json.Unmarshal(out, &o); err != nil {
			c.t.Fatalf("op: %s", out)
		}
		if o.State == "done" {
			return o
		}
		time.Sleep(50 * time.Millisecond)
	}
	c.t.Fatalf("op %s did not finish", id)
	return op{}
}

func TestSessionIsRequiredAndTheCodeWorksOnce(t *testing.T) {
	c := start(t, testkit.New(t), nil)
	if code, _, _ := c.do("GET", "/api/status", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", code)
	}
	c.signIn()
	if code, _, _ := c.do("POST", "/api/session", map[string]string{"code": c.srv.Code()}, nil); code != http.StatusUnauthorized {
		t.Fatalf("second use of the code: %d", code)
	}
	code, body, _ := c.do("GET", "/api/status", nil, nil)
	var env struct{ Kind string }
	if code != 200 || json.Unmarshal(body, &env) != nil || env.Kind != "acp.status" {
		t.Fatalf("status: %d %s", code, body)
	}
}

func TestForeignHostAndOriginAreRefused(t *testing.T) {
	c := start(t, testkit.New(t), nil)
	c.signIn()
	if code, _, _ := c.do("GET", "/", nil, map[string]string{"Host": "evil.example:80"}); code != http.StatusForbidden {
		t.Fatalf("foreign host: %d", code)
	}
	if code, _, _ := c.do("GET", "/api/status", nil, map[string]string{"Origin": "http://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", code)
	}
	if code, _, _ := c.do("GET", "/api/status", nil, map[string]string{"Origin": c.base}); code != 200 {
		t.Fatalf("own origin: %d", code)
	}
}

func TestIndexCarriesTheSecurityHeaders(t *testing.T) {
	c := start(t, testkit.New(t), nil)
	code, body, h := c.do("GET", "/", nil, nil)
	if code != 200 || !strings.Contains(string(body), "<title>") {
		t.Fatalf("index: %d", code)
	}
	if h.Get("Content-Security-Policy") != csp {
		t.Fatalf("csp %q", h.Get("Content-Security-Policy"))
	}
	if csp != "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" {
		t.Fatalf("csp changed: %s", csp)
	}
	for k, v := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store"} {
		if h.Get(k) != v {
			t.Errorf("%s = %q", k, h.Get(k))
		}
	}
	if code, _, h := c.do("GET", "/fonts/lexend.woff", nil, nil); code != 200 || h.Get("Content-Type") != "font/woff" {
		t.Fatalf("font: %d %s", code, h.Get("Content-Type"))
	}
}

func TestRunRejectsUnknownKindsAndBadArgs(t *testing.T) {
	f := testkit.New(t)
	c := start(t, f, nil)
	c.signIn()
	for _, body := range []map[string]any{
		{"kind": "nope"},
		{"kind": "skills.preview", "args": map[string]any{"id": "../x", "add": []string{"herdr"}}},
		{"kind": "skills.preview", "args": map[string]any{"id": "demo", "add": []string{"--apply"}}},
		{"kind": "link.preview", "args": map[string]any{"path": "/etc"}},
		{"kind": "add.preview", "args": map[string]any{"path": "relative/dir"}},
	} {
		if code, out, _ := c.do("POST", "/api/run", body, nil); code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", body, code, out)
		}
	}
	big := strings.Repeat("x", 70<<10)
	if code, _, _ := c.do("POST", "/api/run", map[string]any{"kind": "scan", "pad": big}, nil); code != http.StatusRequestEntityTooLarge && code != http.StatusBadRequest {
		t.Errorf("oversized body: %d", code)
	}
}

func TestPreviewThenApplyNeedsAFreshDigest(t *testing.T) {
	f := testkit.New(t)
	c := start(t, f, nil)
	c.signIn()
	args := map[string]any{"id": "demo", "remove": []string{"herdr"}}
	code, prev := c.run("skills.preview", args, "")
	if code != http.StatusAccepted || prev.Envelope["kind"] != "acp.skills.set" || prev.Digest == "" {
		t.Fatalf("preview: %d %+v", code, prev)
	}
	if code, _ := c.run("skills.apply", args, ""); code != http.StatusBadRequest {
		t.Fatalf("apply without digest: %d", code)
	}
	if code, _ := c.run("skills.apply", args, "stale"); code != http.StatusConflict {
		t.Fatalf("apply with a stale digest: %d", code)
	}
	// The manifest changes between preview and apply: the server previews again and refuses.
	f.Manifest("demo", `{"id": "demo", "skills": [{"name": "herdr", "runtimes": ["claude"]}],
  "agentkit": {"kit": "engineer", "channel": "beta", "targets": ["claude-code", "codex"], "skills": "all"}}`)
	if code, _ := c.run("skills.apply", args, prev.Digest); code != http.StatusConflict {
		t.Fatalf("apply after the manifest changed: %d", code)
	}
	_, prev = c.run("skills.preview", args, "")
	code, done := c.run("skills.apply", args, prev.Digest)
	if code != http.StatusAccepted || done.Envelope["exit_code"] != float64(0) {
		t.Fatalf("apply: %d %+v", code, done)
	}
	if strings.Contains(f.Read(filepath.Join(f.CP, "projects/demo.json")), "herdr") {
		t.Fatal("apply did not remove herdr")
	}
}

func TestOneMutationAtATime(t *testing.T) {
	f := testkit.New(t)
	release := make(chan struct{})
	self := core.SelfRunner(acpBin, f.Base)
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "global" && !slices.Contains(args, "--check") {
			<-release
		}
		return self(ctx, args...)
	}
	c := start(t, f, run)
	c.signIn()
	_, g := c.run("global.preview", nil, "")
	code, out, _ := c.do("POST", "/api/run", map[string]any{"kind": "global.apply", "digest": g.Digest}, nil)
	if code != http.StatusAccepted {
		t.Fatalf("global.apply: %d %s", code, out)
	}
	var started struct{ Op string }
	json.Unmarshal(out, &started)
	all := map[string]any{"all": true}
	_, p := c.run("sync.preview", all, "")
	if p.Digest == "" {
		t.Fatalf("sync preview: %+v", p)
	}
	if code, _ := c.run("sync", all, p.Digest); code != http.StatusConflict {
		t.Fatalf("second mutation while one runs: %d", code)
	}
	close(release)
	// The held apply still writes into the fixture; let it finish before cleanup.
	c.wait(started.Op)
}

func TestScanResultIsKeptAndServedAsJSON(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.Home, ".claude.json"), `{"mcpServers": {"x": {"command": "<img src=x onerror=alert(1)>"}}}`)
	c := start(t, f, nil)
	c.signIn()
	if code, _, _ := c.do("GET", "/api/scan", nil, nil); code != http.StatusNotFound {
		t.Fatalf("scan before the first run: %d", code)
	}
	if code, o := c.run("scan", nil, ""); code != http.StatusAccepted || o.Envelope["kind"] != "acp.scan" {
		t.Fatalf("scan: %d %+v", code, o)
	}
	code, body, h := c.do("GET", "/api/scan", nil, nil)
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/json") || !strings.Contains(string(body), "mcp_command_missing") {
		t.Fatalf("last scan: %d %s %s", code, h.Get("Content-Type"), body)
	}
	if !json.Valid(body) {
		t.Fatal("scan is not JSON")
	}
	if code, body, _ := c.do("GET", "/api/hooks", nil, nil); code != 200 || !strings.Contains(string(body), `"mcp"`) {
		t.Fatalf("hooks: %d %s", code, body)
	}
}

func TestEveryApplyKindHasAPreviewAndADescribedCommand(t *testing.T) {
	f := testkit.New(t)
	out, err := core.SelfRunner(acpBin, f.Base)(context.Background(), "describe", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data struct {
			Commands []struct{ Name string } `json:"commands"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &env); err != nil || len(env.Data.Commands) == 0 {
		t.Fatalf("describe: %v %s", err, out)
	}
	described := map[string]bool{}
	for _, c := range env.Data.Commands {
		described[c.Name] = true
	}
	sample := args{ID: "demo", Path: f.WT, Add: []string{"herdr"}}
	for name, k := range kinds {
		argv := k.argv(sample)
		if !described[argv[0]] && !described[argv[0]+" "+argv[1]] {
			t.Errorf("%s runs %v, which describe does not list", name, argv)
		}
		if !k.mutation {
			continue
		}
		p, ok := kinds[k.preview]
		if !ok || p.mutation {
			t.Errorf("%s has no read-only preview kind (%q)", name, k.preview)
		}
	}
}

// fakeRun answers each acp command from a table of envelopes keyed by the command's first words.
func fakeRun(envs map[string]string) core.Runner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		for k, v := range envs {
			if strings.HasPrefix(strings.Join(args, " "), k) {
				return []byte(v), nil
			}
		}
		return []byte(`{"schema_version":1,"kind":"acp.x","data":{"text":""},"exit_code":0}`), nil
	}
}

func TestEnvelopesStayValidJSONWithSecretsHidden(t *testing.T) {
	f := testkit.New(t)
	c := start(t, f, fakeRun(map[string]string{"status": `{"schema_version":1,"kind":"acp.status",` +
		`"data":{"has_token":true,"text":"run with password=\"hunter2\" now"},"exit_code":0}`}))
	c.signIn()
	code, body, _ := c.do("GET", "/api/status", nil, nil)
	if code != 200 || !json.Valid(body) {
		t.Fatalf("status: %d %s", code, body)
	}
	if strings.Contains(string(body), "hunter2") || !strings.Contains(string(body), `"has_token":true`) {
		t.Fatalf("redaction: %s", body)
	}
	if _, o := c.run("scan", nil, ""); o.Envelope == nil {
		t.Fatalf("op lost its envelope: %+v", o)
	}
}

func TestAFailedPreviewCannotBeApplied(t *testing.T) {
	f := testkit.New(t)
	c := start(t, f, fakeRun(map[string]string{"kit add demo": `{"schema_version":1,"kind":"acp.kit",` +
		`"data":{"text":""},"exit_code":1,"error":"demo already uses AgentKit engineer"}`}))
	c.signIn()
	_, o := c.run("kit.preview", map[string]any{"id": "demo"}, "")
	if o.Digest != "" {
		t.Fatalf("a failed preview got a digest: %+v", o)
	}
}

func TestUpgradeCarriesTheChannelAndKit(t *testing.T) {
	a := args{Channel: "stable", Kit: "engineer"}
	for kind, want := range map[string]string{
		"upgrade.preview": "upgrade --dry-run --channel stable --kit engineer",
		"upgrade":         "upgrade --channel stable --kit engineer",
	} {
		if err := kinds[kind].check(nil, a); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if got := strings.Join(kinds[kind].argv(a), " "); got != want {
			t.Errorf("%s: %q, want %q", kind, got, want)
		}
	}
	if kinds["upgrade"].check(nil, args{Channel: "--yes"}) == nil {
		t.Error("a flag passed as the channel")
	}
	if kinds["upgrade"].check(nil, args{Channel: "nightly"}) == nil {
		t.Error("a channel the CLI does not know was accepted")
	}
}
