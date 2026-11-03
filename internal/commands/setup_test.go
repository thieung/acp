package commands

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

const fakeInstaller = `mkdir -p "$AK_INSTALL_DIR"
printf '#!/bin/sh\necho "ak 3.0.0-beta.99 %s"\n' "$AK_CHANNEL" > "$AK_INSTALL_DIR/ak"
chmod +x "$AK_INSTALL_DIR/ak"
echo installed > "$AK_INSTALL_DIR/ran"
`

func installerEnv(t *testing.T, f *testkit.Fixture) (bindir, script string) {
	bindir, script = filepath.Join(f.Base, "akbin"), filepath.Join(f.Base, "install.sh")
	f.Write(script, fakeInstaller)
	t.Setenv("ACP_INSTALLER_URL", "file://"+script)
	t.Setenv("AK_INSTALL_DIR", bindir)
	return bindir, script
}

func TestInstallAkRunsTheDownloadedInstaller(t *testing.T) {
	f := testkit.New(t)
	bindir, _ := installerEnv(t, f)
	code, plan := acpJSON(t, f, "setup", "install-ak")
	if code != 0 || plan["applied"] != false {
		t.Fatalf("plan: %d %v", code, plan)
	}
	if env := plan["env"].(map[string]any); len(env) != 1 || env["AK_CHANNEL"] != "beta" {
		t.Fatalf("env %v", env)
	}
	if f.Exists(filepath.Join(f.CP, "runtime/installers")) || f.Exists(filepath.Join(bindir, "ran")) {
		t.Fatal("the plan downloaded or ran something")
	}
	code, done := acpJSON(t, f, "setup", "install-ak", "--apply")
	if code != 0 {
		t.Fatalf("apply: %d %v", code, done)
	}
	if done["version"] != "ak 3.0.0-beta.99 beta" || len(done["sha256"].(string)) != 64 || done["doctor_ok"] != true {
		t.Fatalf("result %v", done)
	}
}

func TestInstallAkChecksTheExpectedSha256(t *testing.T) {
	f := testkit.New(t)
	bindir, script := installerEnv(t, f)
	t.Setenv("ACP_INSTALLER_SHA256", strings.Repeat("0", 64))
	if code, data := acpJSON(t, f, "setup", "install-ak", "--apply"); code != 1 || !strings.Contains(data["error"].(string), "sha256") {
		t.Fatalf("wrong sha256: %d %v", code, data)
	}
	if f.Exists(filepath.Join(bindir, "ran")) {
		t.Fatal("the installer ran despite a sha256 mismatch")
	}
	sum := sha256.Sum256([]byte(f.Read(script)))
	t.Setenv("ACP_INSTALLER_SHA256", hex.EncodeToString(sum[:]))
	if code, data := acpJSON(t, f, "setup", "install-ak", "--apply"); code != 0 {
		t.Fatalf("matching sha256: %d %v", code, data)
	}
}

func TestInstallAkRejectsPlainHTTP(t *testing.T) {
	f := testkit.New(t)
	bindir, _ := installerEnv(t, f)
	t.Setenv("ACP_INSTALLER_URL", "http://example.invalid/install.sh")
	if r := acp(t, f, "", "setup", "install-ak", "--apply"); r.code != 1 || !strings.Contains(r.stderr, "https") {
		t.Fatalf("http: %d %q", r.code, r.stderr)
	}
	if f.Exists(filepath.Join(bindir, "ran")) {
		t.Fatal("ran")
	}
}

func TestSetupCheckReportsAkLoginAndDoctor(t *testing.T) {
	f := testkit.New(t)
	ak := filepath.Join(f.Base, "bin/ak-setup")
	f.Executable(ak, `#!/bin/sh
case "$1" in
  --version) echo "ak 3.0.0-beta.36" ;;
  whoami) echo '{"data": {"authenticated": true, "authMethod": "device"}}' ;;
  doctor) echo '{"data": {"healthy": true}}' ;;
esac
`)
	t.Setenv("ACP_AK", ak)
	code, data := acpJSON(t, f, "setup")
	if code != 0 {
		t.Fatalf("exit %d %v", code, data)
	}
	if data["ak"].(map[string]any)["version"] != "3.0.0-beta.36" || data["healthy"] != true ||
		data["login"].(map[string]any)["method"] != "device" || data["channel"] != "beta" {
		t.Fatalf("data %v", data)
	}
	t.Setenv("ACP_AK", filepath.Join(f.Base, "missing"))
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("AK_INSTALL_DIR", filepath.Join(f.Base, "nowhere"))
	r := acp(t, f, "", "setup")
	if r.code != 1 || !strings.Contains(r.stdout, "not installed") {
		t.Fatalf("missing ak: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestSetupLoginNeedsATerminal(t *testing.T) {
	f := testkit.New(t)
	r := acp(t, f, "", "setup", "login")
	if r.code != 1 || !strings.Contains(r.stderr, "ak onboard") || !strings.Contains(r.stderr, "--api-key-stdin") {
		t.Fatalf("login without a terminal: %d %q", r.code, r.stderr)
	}
}

// fakeKey is an obvious fake in the shape of an AgentKit key, so Redact would catch it too.
const fakeKey = "ak_live_FAKEKEY0000000000"

func TestLoginWithAPIKeyFromStdin(t *testing.T) {
	f := testkit.New(t)
	r := acp(t, f, fakeKey+"\n", "setup", "login", "--api-key-stdin", "--json")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	login, _ := envelope(t, r.stdout)["data"].(map[string]any)["login"].(map[string]any)
	if login["authenticated"] != true || login["method"] != "api_key" {
		t.Fatalf("login %v", login)
	}
	calls := f.Read(filepath.Join(f.Base, ".ak-calls"))
	for name, s := range map[string]string{"stdout": r.stdout, "stderr": r.stderr, ".ak-calls": calls} {
		if strings.Contains(s, fakeKey) {
			t.Errorf("the key leaks into %s: %q", name, s)
		}
	}
	if !strings.Contains(calls, "login --api-key-stdin --json --no-interactive") ||
		strings.Contains(calls, "--api-key ") || strings.Contains(calls, "AGENTKIT_API_KEY") {
		t.Fatalf(".ak-calls %q", calls)
	}
	sum := sha256.Sum256([]byte(fakeKey))
	if got := f.Read(filepath.Join(f.Home, ".ak-login")); strings.TrimSpace(got) != hex.EncodeToString(sum[:]) {
		t.Fatalf("ak did not get the trimmed key on stdin: %q", got)
	}
	if r := acp(t, f, fakeKey, "setup", "login", "--api-key-stdin"); r.code != 0 || !strings.Contains(r.stdout, "signed in") {
		t.Fatalf("text: %d %q %q", r.code, r.stdout, r.stderr)
	}
}

func TestLoginBadKeyFailsWithoutEchoingIt(t *testing.T) {
	f := testkit.New(t)
	key := "ak_live_badFAKEKEY000000000"
	for _, args := range [][]string{{"--json"}, nil} {
		r := acp(t, f, key, append([]string{"setup", "login", "--api-key-stdin"}, args...)...)
		if r.code != 1 {
			t.Fatalf("bad key %v: exit %d\n%s%s", args, r.code, r.stdout, r.stderr)
		}
		if strings.Contains(r.stdout, key) || strings.Contains(r.stderr, key) || strings.Contains(r.stdout+r.stderr, "<stdin>") {
			t.Fatalf("ak's output or the key leaks %v: %q %q", args, r.stdout, r.stderr)
		}
		if args != nil {
			if msg, _ := envelope(t, r.stdout)["error"].(string); !strings.HasPrefix(msg, "sign-in failed") {
				t.Fatalf("error %q", msg)
			}
		}
	}
	if f.Exists(filepath.Join(f.Home, ".ak-login")) {
		t.Fatal("a bad key signed in")
	}
}

func TestLoginRejectsEmptyAndHugeKey(t *testing.T) {
	f := testkit.New(t)
	for name, key := range map[string]string{"empty": " \n", "huge": "ak_live_" + strings.Repeat("A", 4096),
		"two words": "ak_live_FAKE KEY0000000000"} {
		if r := acp(t, f, key, "setup", "login", "--api-key-stdin", "--json"); r.code != 2 {
			t.Errorf("%s key: exit %d %q", name, r.code, r.stdout)
		}
	}
	if f.Exists(filepath.Join(f.Base, ".ak-calls")) {
		t.Fatalf("ak was called: %q", f.Read(filepath.Join(f.Base, ".ak-calls")))
	}
}

func TestAPIKeyStdinOnlyForLogin(t *testing.T) {
	f := testkit.New(t)
	for _, args := range [][]string{{"setup", "--api-key-stdin"}, {"setup", "install-ak", "--api-key-stdin"}} {
		if r := acp(t, f, fakeKey, args...); r.code != 2 || !strings.Contains(r.stderr, "only with `acp setup login`") {
			t.Errorf("%v: exit %d %q", args, r.code, r.stderr)
		}
	}
	if f.Exists(filepath.Join(f.Base, ".ak-calls")) {
		t.Fatal("ak was called")
	}
}

func TestBinaryCarriesTheInstallerMarker(t *testing.T) {
	b, err := os.ReadFile(acpBin)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(BinaryMarker)) || BinaryMarker != "ACP-BINARY-v1" {
		t.Fatal("install.sh cannot recognize this binary: ACP-BINARY-v1 is not in it")
	}
}

func TestInstallerDownloadDoesNotFollowARedirectOffHTTPS(t *testing.T) {
	to := func(raw string) *http.Request {
		u, _ := url.Parse(raw)
		return &http.Request{URL: u}
	}
	if err := httpsOnlyRedirect(to("http://example.invalid/install.sh"), nil); err == nil {
		t.Fatal("followed a redirect to plain http")
	}
	if err := httpsOnlyRedirect(to("https://example.invalid/install.sh"), nil); err != nil {
		t.Fatalf("an https redirect was refused: %v", err)
	}
	if err := httpsOnlyRedirect(to("https://example.invalid/x"), make([]*http.Request, 10)); err == nil {
		t.Fatal("no limit on redirects")
	}
}

// linkLines returns the change, ok and issue lines of a --json envelope's logs.
func linkLines(t *testing.T, stdout string) (changes, ok []string) {
	t.Helper()
	logs, _ := envelope(t, stdout)["data"].(map[string]any)["logs"].([]any)
	for _, l := range logs {
		for _, c := range l.(map[string]any)["changes"].([]any) {
			changes = append(changes, c.(string))
		}
		for _, c := range l.(map[string]any)["ok"].([]any) {
			ok = append(ok, c.(string))
		}
	}
	return changes, ok
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLinkCLIPreviewsThenLinksThisBinary(t *testing.T) {
	f := testkit.New(t)
	bin, run := installedAcp(t, f, filepath.Join(f.Base, "app"))
	self := realPath(t, bin)
	// link-cli links the binary running it, never ACP_BIN.
	t.Setenv("ACP_BIN", filepath.Join(f.Base, "elsewhere/acp"))
	dest := filepath.Join(f.Home, ".local/bin/acp")
	r := run("", "setup", "link-cli", "--json")
	if r.code != 0 {
		t.Fatalf("preview: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if changes, _ := linkLines(t, r.stdout); len(changes) != 1 || changes[0] != "+ "+dest+" -> "+self {
		t.Fatalf("preview changes %q", changes)
	}
	if _, err := os.Lstat(dest); err == nil {
		t.Fatal("the preview wrote the link")
	}
	if r := run("", "setup", "link-cli", "--apply"); r.code != 0 {
		t.Fatalf("apply: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if got, _ := os.Readlink(dest); got != self {
		t.Fatalf("link %q, want %q", got, self)
	}
	r = run("", "setup", "link-cli", "--apply", "--json")
	changes, ok := linkLines(t, r.stdout)
	if r.code != 0 || len(changes) != 0 || len(ok) != 1 || !strings.Contains(ok[0], "already") {
		t.Fatalf("second apply: %d changes %q ok %q", r.code, changes, ok)
	}
}

func TestLinkCLIReplacesOnlyAnotherAcp(t *testing.T) {
	f := testkit.New(t)
	bin, run := installedAcp(t, f, filepath.Join(f.Base, "app"))
	self := realPath(t, bin)
	dest := filepath.Join(f.Home, ".local/bin/acp")
	older := filepath.Join(f.Base, "old/acp")
	f.Executable(older, "#!/bin/sh\n# "+BinaryMarker+"\n")
	legacy := filepath.Join(f.Base, "cp/scripts/acp")
	f.Executable(legacy, "#!/usr/bin/env python3\n")
	moved := filepath.Join(f.Base, "Downloads/acp.app/Contents/Helpers/acp") // the app was moved or deleted
	for name, target := range map[string]string{"another acp": older, "the Python acp": legacy, "a moved acp.app": moved} {
		os.Remove(dest)
		f.Symlink(target, dest)
		if r := run("", "setup", "link-cli", "--apply"); r.code != 0 {
			t.Fatalf("%s: %d\n%s%s", name, r.code, r.stdout, r.stderr)
		}
		if got, _ := os.Readlink(dest); got != self {
			t.Fatalf("%s: link %q", name, got)
		}
	}
	foreign := filepath.Join(f.Base, "tool")
	f.Executable(foreign, "#!/bin/sh\necho mine\n")
	for _, target := range []string{foreign, filepath.Join(f.Base, "gone/acp")} {
		os.Remove(dest)
		f.Symlink(target, dest)
		for _, args := range [][]string{nil, {"--apply"}} {
			r := run("", append([]string{"setup", "link-cli"}, args...)...)
			if r.code != 1 || !strings.Contains(r.stderr, dest) {
				t.Fatalf("link to %s %v: %d %q", target, args, r.code, r.stderr)
			}
		}
		if got, _ := os.Readlink(dest); got != target {
			t.Fatalf("a link to %s was changed to %q", target, got)
		}
	}
}

func TestLinkCLIBacksUpACurlInstalledAcp(t *testing.T) {
	f := testkit.New(t)
	bin, run := installedAcp(t, f, filepath.Join(f.Base, "app"))
	dest := filepath.Join(f.Home, ".local/bin/acp")
	old := "#!/bin/sh\n# " + BinaryMarker + " v0.1.0\n"
	f.Executable(dest, old)
	r := run("", "setup", "link-cli", "--json")
	if changes, _ := linkLines(t, r.stdout); r.code != 0 || len(changes) != 1 || !strings.Contains(changes[0], "backup") {
		t.Fatalf("preview: %d %q", r.code, changes)
	}
	if !strings.HasPrefix(f.Read(dest), "#!/bin/sh") {
		t.Fatal("the preview replaced the file")
	}
	if r := run("", "setup", "link-cli", "--apply"); r.code != 0 {
		t.Fatalf("apply: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if got, _ := os.Readlink(dest); got != realPath(t, bin) {
		t.Fatalf("link %q", got)
	}
	backups, _ := filepath.Glob(filepath.Join(f.CP, "runtime/backups/*/acp"))
	if len(backups) != 1 || f.Read(backups[0]) != old {
		t.Fatalf("backups %v", backups)
	}
	if fi, _ := os.Stat(backups[0]); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("the backup lost its execute bit: %v", fi.Mode())
	}
}

func TestLinkCLIRefusesWhatIsNotAcp(t *testing.T) {
	f := testkit.New(t)
	_, run := installedAcp(t, f, filepath.Join(f.Base, "app"))
	dest := filepath.Join(f.Home, ".local/bin/acp")
	f.Executable(dest, "#!/bin/sh\necho my own acp\n")
	if r := run("", "setup", "link-cli", "--apply"); r.code != 1 || !strings.Contains(r.stderr, dest) {
		t.Fatalf("foreign file: %d %q", r.code, r.stderr)
	}
	if f.IsLink(dest) || !strings.Contains(f.Read(dest), "my own acp") {
		t.Fatal("a foreign file was replaced")
	}
	os.RemoveAll(filepath.Join(f.Home, ".local"))
	f.Symlink(filepath.Join(f.Base, "shared-bin"), filepath.Join(f.Home, ".local/bin"))
	os.MkdirAll(filepath.Join(f.Base, "shared-bin"), 0o755)
	if r := run("", "setup", "link-cli", "--apply"); r.code != 1 || !strings.Contains(r.stderr, "symlink") {
		t.Fatalf("symlinked bin folder: %d %q", r.code, r.stderr)
	}
	if exists(filepath.Join(f.Base, "shared-bin/acp")) {
		t.Fatal("wrote through a symlinked ~/.local/bin")
	}
}

func TestLinkCLIRefusesATemporaryBuild(t *testing.T) {
	f := testkit.New(t)
	r := acp(t, f, "", "setup", "link-cli", "--apply")
	if r.code != 1 || !strings.Contains(r.stderr, "temporary") {
		t.Fatalf("temporary build: %d %q", r.code, r.stderr)
	}
	if _, err := os.Lstat(filepath.Join(f.Home, ".local/bin/acp")); err == nil {
		t.Fatal("linked a temporary build")
	}
}

// fakeXcodeSelect answers `-p` as installed when $HOME/.clt exists and records `--install`.
func fakeXcodeSelect(t *testing.T, f *testkit.Fixture) string {
	path := filepath.Join(f.Base, "bin/xcode-select")
	f.Executable(path, `#!/bin/sh
echo "$@" >> "`+f.Base+`/.xcode-calls"
case "$1" in
  -p) [ -e "$HOME/.clt" ] && echo /Library/Developer/CommandLineTools && exit 0
      echo "xcode-select: error: unable to get active developer directory" >&2; exit 2 ;;
  --install) echo "xcode-select: note: install requested" ;;
esac
`)
	t.Setenv("ACP_XCODE_SELECT", path)
	return filepath.Join(f.Base, ".xcode-calls")
}

func TestSetupCheckReportsCLIAndCLT(t *testing.T) {
	f := testkit.New(t)
	bin, run := installedAcp(t, f, filepath.Join(f.Base, "app"))
	fakeXcodeSelect(t, f)
	data := func() map[string]any {
		t.Helper()
		return envelope(t, run("", "setup", "--json").stdout)["data"].(map[string]any)
	}
	d := data()
	cli, _ := d["cli"].(map[string]any)
	if cli["path"] != filepath.Join(f.Home, ".local/bin/acp") || cli["current"] != false || cli["target"] != nil {
		t.Fatalf("cli before linking %v", cli)
	}
	mustOK(t, run("", "setup", "link-cli", "--apply"))
	if cli, _ := data()["cli"].(map[string]any); cli["current"] != true || cli["target"] != realPath(t, bin) {
		t.Fatalf("cli after linking %v", cli)
	}
	clt, has := d["clt"].(map[string]any)
	if runtime.GOOS != "darwin" {
		if has {
			t.Fatalf("clt reported off macOS: %v", clt)
		}
		return
	}
	if clt["installed"] != false {
		t.Fatalf("clt %v", clt)
	}
	f.Write(filepath.Join(f.Home, ".clt"), "")
	if clt, _ := data()["clt"].(map[string]any); clt["installed"] != true {
		t.Fatalf("clt after install %v", clt)
	}
}

func TestInstallCLTOpensApplesInstallerOnlyOnApply(t *testing.T) {
	f := testkit.New(t)
	calls := fakeXcodeSelect(t, f)
	if runtime.GOOS != "darwin" {
		if r := acp(t, f, "", "setup", "install-clt", "--apply"); r.code != 1 || !strings.Contains(r.stderr, "macOS") {
			t.Fatalf("off macOS: %d %q", r.code, r.stderr)
		}
		if f.Exists(calls) {
			t.Fatal("ran xcode-select off macOS")
		}
		return
	}
	r := acp(t, f, "", "setup", "install-clt", "--json")
	if changes, _ := linkLines(t, r.stdout); r.code != 0 || len(changes) != 1 {
		t.Fatalf("plan: %d %q", r.code, changes)
	}
	if strings.Contains(f.Read(calls), "--install") {
		t.Fatal("the plan opened the installer")
	}
	mustOK(t, acp(t, f, "", "setup", "install-clt", "--apply"))
	if !strings.Contains(f.Read(calls), "--install") {
		t.Fatalf("apply did not run xcode-select --install: %q", f.Read(calls))
	}
	f.Write(filepath.Join(f.Home, ".clt"), "")
	os.Remove(calls)
	r = acp(t, f, "", "setup", "install-clt", "--apply", "--json")
	if changes, ok := linkLines(t, r.stdout); r.code != 0 || len(changes) != 0 || len(ok) != 1 {
		t.Fatalf("already installed: %d %q %q", r.code, changes, ok)
	}
	if strings.Contains(f.Read(calls), "--install") {
		t.Fatal("opened the installer although the tools are installed")
	}
}
