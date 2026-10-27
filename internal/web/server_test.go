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
	"syscall"
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
	run := func(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
		if args[0] == "global" && !slices.Contains(args, "--check") {
			<-release
		}
		return self(ctx, stdin, args...)
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
	out, err := core.SelfRunner(acpBin, f.Base)(context.Background(), nil, "describe", "--json")
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
	return func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
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

// fakeKey is an obvious fake in the shape of an AgentKit key; badKey is one the fake ak refuses.
const (
	fakeKey = "ak_live_FAKEKEY0000000000"
	badKey  = "ak_live_badFAKEKEY000000000"
)

func TestLoginEndpointSignsIn(t *testing.T) {
	f := testkit.New(t)
	c := start(t, f, nil)
	c.signIn()
	code, body, _ := c.do("POST", "/api/login", map[string]string{"key": fakeKey}, nil)
	var env struct {
		Data struct {
			Login struct {
				Authenticated bool
				Method        string
			}
		}
	}
	if code != 200 || json.Unmarshal(body, &env) != nil || !env.Data.Login.Authenticated || env.Data.Login.Method != "api_key" {
		t.Fatalf("login: %d %s", code, body)
	}
	if code, body, _ := c.do("GET", "/api/setup", nil, nil); code != 200 || !strings.Contains(string(body), `"authenticated":true`) {
		t.Fatalf("setup after sign-in: %d %s", code, body)
	}
}

func TestLoginEndpointBadKeyIsNotSuccess(t *testing.T) {
	f := testkit.New(t)
	c := start(t, f, nil)
	c.signIn()
	code, body, _ := c.do("POST", "/api/login", map[string]string{"key": badKey}, nil)
	var env struct{ Error string }
	if code != http.StatusBadRequest || json.Unmarshal(body, &env) != nil || !strings.HasPrefix(env.Error, "sign-in failed") {
		t.Fatalf("bad key: %d %s", code, body)
	}
	if strings.Contains(string(body), badKey) {
		t.Fatalf("the key is in the response: %s", body)
	}
}

func TestLoginEndpointNeverKeepsTheKey(t *testing.T) {
	f := testkit.New(t)
	c := start(t, f, nil)
	c.signIn()
	var bodies []string
	for _, key := range []string{badKey, fakeKey} {
		_, body, _ := c.do("POST", "/api/login", map[string]string{"key": key}, nil)
		bodies = append(bodies, string(body))
	}
	c.run("scan", nil, "")
	for _, p := range []string{"/api/setup", "/api/status", "/api/scan"} {
		_, body, _ := c.do("GET", p, nil, nil)
		bodies = append(bodies, string(body))
	}
	c.srv.mu.Lock()
	ops, _ := json.Marshal(c.srv.ops)
	kept := fmt.Sprint(c.srv.sessions, c.srv.order) + string(ops) + string(c.srv.lastScan)
	c.srv.mu.Unlock()
	for name, s := range map[string]string{"responses": strings.Join(bodies, "\n"), "server state": kept,
		".ak-calls": f.Read(filepath.Join(f.Base, ".ak-calls"))} {
		for _, key := range []string{fakeKey, badKey} {
			if strings.Contains(s, key) {
				t.Errorf("%s keep the key %s: %s", name, key, s)
			}
		}
	}
}

func TestLoginEndpointRequiresSession(t *testing.T) {
	called := false
	c := start(t, testkit.New(t), func(context.Context, []byte, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if code, _, _ := c.do("POST", "/api/login", map[string]string{"key": fakeKey}, nil); code != http.StatusUnauthorized || called {
		t.Fatalf("login without a session: %d (runner called: %v)", code, called)
	}
}

func TestLoginEndpointBusy(t *testing.T) {
	called := false
	c := start(t, testkit.New(t), func(context.Context, []byte, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	c.signIn()
	c.srv.mu.Lock()
	c.srv.busy = true
	c.srv.mu.Unlock()
	if code, _, _ := c.do("POST", "/api/login", map[string]string{"key": fakeKey}, nil); code != http.StatusConflict || called {
		t.Fatalf("login while a change runs: %d (runner called: %v)", code, called)
	}
}

func TestLoginEndpointRejectsEmptyKey(t *testing.T) {
	called := false
	c := start(t, testkit.New(t), func(context.Context, []byte, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	c.signIn()
	for _, body := range []any{map[string]string{"key": ""}, map[string]string{"key": "  \n"}, map[string]any{}, "nope"} {
		if code, out, _ := c.do("POST", "/api/login", body, nil); code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", body, code, out)
		}
	}
	if called {
		t.Fatal("the runner ran without a key")
	}
}

func TestLoginEndpointClearsBusyOnError(t *testing.T) {
	var got []string
	var stdin []byte
	c := start(t, testkit.New(t), func(_ context.Context, in []byte, args ...string) ([]byte, error) {
		got, stdin = args, slices.Clone(in)
		return nil, fmt.Errorf("acp %s: exit status 2", args[0])
	})
	c.signIn()
	if code, body, _ := c.do("POST", "/api/login", map[string]string{"key": fakeKey}, nil); code != http.StatusBadGateway {
		t.Fatalf("runner error: %d %s", code, body)
	}
	if strings.Join(got, " ") != "setup login --api-key-stdin --json" || string(stdin) != fakeKey {
		t.Fatalf("runner got %v with stdin %q", got, stdin)
	}
	c.srv.mu.Lock()
	busy := c.srv.busy
	c.srv.mu.Unlock()
	if busy {
		t.Fatal("busy stays set after a failed sign-in")
	}
}

func TestLoginEndpointSurvivesClientCancel(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	ctxErr := make(chan error, 1)
	c := start(t, testkit.New(t), func(ctx context.Context, _ []byte, _ ...string) ([]byte, error) {
		close(entered)
		<-release
		ctxErr <- ctx.Err()
		return []byte(`{"schema_version":1,"kind":"acp.setup.login","data":{"login":{"authenticated":true}},"exit_code":0}`), nil
	})
	c.signIn()
	ctx, cancel := context.WithCancel(context.Background())
	b, _ := json.Marshal(map[string]string{"key": fakeKey})
	req, _ := http.NewRequestWithContext(ctx, "POST", c.base+"/api/login", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+c.token)
	done := make(chan struct{})
	go func() {
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
		close(done)
	}()
	select {
	case <-entered:
	case <-done:
		t.Fatal("the request ended before the sign-in ran")
	}
	cancel()
	<-done
	time.Sleep(100 * time.Millisecond) // let the server notice the closed connection
	close(release)
	if err := <-ctxErr; err != nil {
		t.Fatalf("the sign-in was cancelled with the request: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.srv.mu.Lock()
		busy := c.srv.busy
		c.srv.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("busy stays set after the sign-in finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLinkCLIPreviewsAndAppliesFromTheDashboard(t *testing.T) {
	f := testkit.New(t)
	// The server's own binary is what gets linked, so it must live outside a temporary folder.
	t.Setenv("TMPDIR", filepath.Join(f.Base, "tmp"))
	os.MkdirAll(filepath.Join(f.Base, "tmp"), 0o755)
	data, err := os.ReadFile(acpBin)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(f.Base, "app/acp")
	f.Write(bin, "")
	if err := os.WriteFile(bin, data, 0o755); err != nil || os.Chmod(bin, 0o755) != nil {
		t.Fatal(err)
	}
	self, _ := filepath.EvalSymlinks(bin)
	dest := filepath.Join(f.Home, ".local/bin/acp")
	c := start(t, f, core.SelfRunner(bin, f.Base))
	c.signIn()
	code, prev := c.run("setup.link-cli.preview", nil, "")
	d, _ := prev.Envelope["data"].(map[string]any)
	logs, _ := d["logs"].([]any)
	if code != http.StatusAccepted || prev.Digest == "" || len(logs) != 1 ||
		fmt.Sprint(logs[0].(map[string]any)["changes"]) != "[+ "+dest+" -> "+self+"]" {
		t.Fatalf("preview: %d %+v", code, prev)
	}
	if code, done := c.run("setup.link-cli", nil, prev.Digest); code != http.StatusAccepted || done.Envelope["exit_code"] != float64(0) {
		t.Fatalf("apply: %d %+v", code, done)
	}
	if got, _ := os.Readlink(dest); got != self {
		t.Fatalf("link %q, want %q", got, self)
	}
	for kind, want := range map[string]string{"setup.install-clt.preview": "setup install-clt",
		"setup.install-clt": "setup install-clt --apply", "setup.link-cli": "setup link-cli --apply"} {
		if got := strings.Join(kinds[kind].argv(args{}), " "); got != want {
			t.Errorf("%s runs %q, want %q", kind, got, want)
		}
	}
}

func TestConnectPreviewsAndAppliesFromTheDashboard(t *testing.T) {
	f := testkit.New(t)
	t.Setenv("TMPDIR", filepath.Join(f.Base, "tmp"))
	os.MkdirAll(filepath.Join(f.Base, "tmp"), 0o755)
	bin := filepath.Join(f.Base, "app/acp")
	f.Write(bin, "")
	data, err := os.ReadFile(acpBin)
	if err != nil || os.WriteFile(bin, data, 0o755) != nil || os.Chmod(bin, 0o755) != nil {
		t.Fatal(err)
	}
	t.Setenv("ACP_BIN", bin)
	c := start(t, f, nil)
	c.signIn()
	code, prev := c.run("connect.preview", nil, "")
	d, _ := prev.Envelope["data"].(map[string]any)
	if code != http.StatusAccepted || prev.Digest == "" || fmt.Sprint(d["agents"]) !=
		"[map[cli:"+f.Claude+" name:claude state:missing] map[cli:"+f.Codex+" name:codex state:missing]]" {
		t.Fatalf("preview: %d %+v", code, prev)
	}
	if f.Exists(filepath.Join(f.Home, ".fake-claude-calls")) {
		t.Fatal("the preview ran claude")
	}
	if code, done := c.run("connect.apply", nil, prev.Digest); code != http.StatusAccepted || done.Envelope["exit_code"] != float64(0) {
		t.Fatalf("apply: %d %+v", code, done)
	}
	if got := f.Read(filepath.Join(f.Home, ".fake-claude-calls")); got != "mcp add --scope user acp -- "+bin+" mcp\n" {
		t.Fatalf("claude calls %q", got)
	}
	for kind, want := range map[string]string{"connect.preview": "connect", "connect.apply": "connect --apply"} {
		if got := strings.Join(kinds[kind].argv(args{}), " "); got != want {
			t.Errorf("%s runs %q, want %q", kind, got, want)
		}
	}
}

func TestShutdownWaitsForRunningOp(t *testing.T) {
	f := testkit.New(t)
	started, release := make(chan struct{}, 1), make(chan struct{})
	c := start(t, f, func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		if args[0] == "scan" {
			started <- struct{}{}
			<-release
		}
		return []byte(`{"schema_version":1,"kind":"acp.x","data":{"text":""},"exit_code":0}`), nil
	})
	c.signIn()
	if c.srv.Running() {
		t.Fatal("running before any operation")
	}
	if code, out, _ := c.do("POST", "/api/run", map[string]any{"kind": "scan"}, nil); code != http.StatusAccepted {
		t.Fatalf("run: %d %s", code, out)
	}
	<-started
	if !c.srv.Running() {
		t.Fatal("Running is false while an operation runs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.srv.Wait(ctx); err != context.DeadlineExceeded {
		t.Fatalf("Wait returned %v before the operation ended", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- c.srv.Wait(context.Background()) }()
	select {
	case err := <-waited:
		t.Fatalf("Wait returned %v before the operation ended", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after the operation ended")
	}
	if c.srv.Running() {
		t.Fatal("Running is true after the operation ended")
	}
	if err := c.srv.Wait(context.Background()); err != nil {
		t.Fatalf("Wait with nothing running: %v", err)
	}
}

func TestShutdownWaitsForASignIn(t *testing.T) {
	f := testkit.New(t)
	started, release := make(chan struct{}, 1), make(chan struct{})
	c := start(t, f, func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		started <- struct{}{}
		<-release
		return []byte(`{"schema_version":1,"kind":"acp.setup.login","data":{"login":{"authenticated":true}},"exit_code":0}`), nil
	})
	c.signIn()
	go c.do("POST", "/api/login", map[string]string{"key": "ak_live_FAKEKEY0000000000"}, nil)
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if !c.srv.Running() || c.srv.Wait(ctx) != context.DeadlineExceeded {
		t.Fatal("a running sign-in does not count as running")
	}
	close(release)
	if err := c.srv.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The web sign-in must outlast everything `acp setup login` may wait for, so its own deadline never
// fires while ak is still working.
func TestLoginTimeoutOutlastsTheChild(t *testing.T) {
	// Each Exec may add about 4 s after its deadline: SIGTERM, a 2 s wait, then the group kill.
	if inner := core.AKLoginTimeout + core.AKWhoamiTimeout + 10*time.Second; loginTimeout <= inner {
		t.Fatalf("loginTimeout %v must exceed the child's own bounds %v", loginTimeout, inner)
	}
}

// When the web sign-in gives up, ak must be gone with it and the message must read well.
func TestLoginTimeoutStopsAk(t *testing.T) {
	f := testkit.New(t)
	pidFile := filepath.Join(f.Base, "ak.pid")
	f.Executable(f.AK, "#!/bin/sh\necho $$ > '"+pidFile+"'\nexec sleep 30\n")
	old := loginTimeout
	loginTimeout = 1500 * time.Millisecond
	t.Cleanup(func() { loginTimeout = old })
	c := start(t, f, core.SelfRunner(acpBin, f.Base))
	c.signIn()
	code, body, _ := c.do("POST", "/api/login", map[string]string{"key": fakeKey}, nil)
	if code != http.StatusGatewayTimeout || !strings.Contains(string(body), "quá lâu") || strings.Contains(string(body), "0s") {
		t.Fatalf("login past its deadline: %d %s", code, body)
	}
	var pid int
	fmt.Sscan(f.Read(pidFile), &pid)
	if pid == 0 {
		t.Fatal("the fake ak did not start")
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	for i := 0; syscall.Kill(pid, 0) == nil; i++ {
		if i > 40 {
			t.Fatalf("ak %d outlived the sign-in", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
