package commands

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	if r := acp(t, f, "", "setup", "login"); r.code != 1 || !strings.Contains(r.stderr, "ak onboard") {
		t.Fatalf("login without a terminal: %d %q", r.code, r.stderr)
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
