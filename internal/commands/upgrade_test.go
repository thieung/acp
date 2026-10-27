package commands

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thieung/acp/internal/testkit"
)

func pinOf(t *testing.T, f *testkit.Fixture, channel, kit string) any {
	t.Helper()
	ch, _ := readMap(t, filepath.Join(f.CP, "agentkit-releases.lock.json"))["channels"].(map[string]any)
	kits, _ := ch[channel].(map[string]any)
	return kits[kit]
}

func TestUpgradePinsLatestWithoutTheBinaryStep(t *testing.T) {
	f := testkit.New(t)
	mustOK(t, acp(t, f, "", "upgrade", "--skip-binary", "--no-sync"))
	if v := pinOf(t, f, "beta", "engineer"); v != "9.9.9-beta.1" {
		t.Fatalf("pin %v", v)
	}
	if strings.Contains(f.Read(filepath.Join(f.Repo, ".ak-calls")), "kit") {
		t.Fatal("--no-sync ran ak kit")
	}
}

func TestUpgradeDryRunWritesNothingAndReportsTheResolvedVersion(t *testing.T) {
	f := testkit.New(t)
	code, data := acpJSON(t, f, "upgrade", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d: %v", code, data)
	}
	if !strings.Contains(data["text"].(string), "pin beta/engineer: 3.0.0-beta.36 -> 9.9.9-beta.1") {
		t.Fatalf("text: %s", data["text"])
	}
	resolved := data["resolved"].(map[string]any)
	if resolved["engineer"] != "9.9.9-beta.1" || pinOf(t, f, "beta", "engineer") != "3.0.0-beta.36" {
		t.Fatalf("resolved %v, pin %v", resolved, pinOf(t, f, "beta", "engineer"))
	}
}

func TestUpgradeVersionPinsExactlyThatVersionAndSyncs(t *testing.T) {
	f := testkit.New(t)
	mustOK(t, acp(t, f, "", "upgrade", "--skip-binary", "--version", "3.0.0-beta.40"))
	if v := pinOf(t, f, "beta", "engineer"); v != "3.0.0-beta.40" {
		t.Fatalf("pin %v, want the requested version even though the changelog has a newer one", v)
	}
	if !strings.Contains(f.Read(filepath.Join(f.Repo, ".ak-calls")), "kit init engineer") {
		t.Fatal("upgrade did not sync the fleet")
	}
	if r := acp(t, f, "", "doctor"); r.code != 0 {
		t.Fatalf("doctor after upgrade:\n%s", r.stdout)
	}
}

func TestUpgradeStopsWhenSelfUpdateFails(t *testing.T) {
	f := testkit.New(t)
	ak := filepath.Join(f.Base, "bin/ak-selfupdate-fails")
	f.Executable(ak, "#!/bin/sh\nif [ \"$1\" = self-update ]; then echo 'update server down' >&2; exit 3; fi\nexec "+f.AK+" \"$@\"\n")
	t.Setenv("ACP_AK", ak)
	r := acp(t, f, "", "upgrade", "--no-sync")
	if r.code != 1 || !strings.Contains(r.stdout, "update server down") {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if v := pinOf(t, f, "beta", "engineer"); v != "3.0.0-beta.36" {
		t.Fatalf("pins changed after a failed self-update: %v", v)
	}
}

func TestUpgradeDropsKitsTheChannelNoLongerUses(t *testing.T) {
	f := testkit.New(t)
	f.Write(filepath.Join(f.CP, "agentkit-releases.lock.json"),
		`{"schemaVersion": 2, "channels": {"beta": {"engineer": "1", "retired": "2"}, "stable": {"engineer": "3"}}}`)
	mustOK(t, acp(t, f, "", "upgrade", "--skip-binary", "--no-sync"))
	if pinOf(t, f, "beta", "retired") != nil || pinOf(t, f, "stable", "engineer") != "3" {
		t.Fatalf("lock: %v", readMap(t, filepath.Join(f.CP, "agentkit-releases.lock.json")))
	}
}
