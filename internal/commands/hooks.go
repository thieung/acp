package commands

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thieung/acp/internal/core"
)

// hookBinary is the acp path the hook runs: ACP_BIN, else ~/.local/bin/acp when it is this binary,
// else this binary, refused when lastingBinary is.
func hookBinary() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	path := os.Getenv("ACP_BIN")
	if path == "" {
		path = self
		if local := filepath.Join(core.HomeDir(), ".local", "bin", "acp"); resolve(local) == resolve(self) {
			path = local
		}
	}
	if !filepath.IsAbs(path) {
		return "", fail("ACP_BIN=%s is not an absolute path", path)
	}
	if !core.IsExecutable(resolve(path)) {
		return "", fail("%s is not an executable file", path)
	}
	if err := lastingBinary(path); err != nil {
		return "", err
	}
	return path, nil
}

// lastingBinary refuses an acp path that will vanish, checked as given and resolved: a temporary
// folder or the Go build cache, or acp inside an app bundle that is not in /Applications or
// ~/Applications (a mounted disk image, the copy macOS runs a quarantined app from, or an app left
// in Downloads that gets tidied away). Hooks, links and MCP entries wired to it would break once it
// is gone. A plain binary on another volume is fine: tools and homes may live there.
func lastingBinary(path string) error {
	temp := []string{os.TempDir(), resolve(os.TempDir())}
	home := core.HomeDir()
	apps := []string{"/Applications", filepath.Join(home, "Applications"), filepath.Join(resolve(home), "Applications")}
	why := func(p string) string {
		inApp := strings.Contains(p, ".app/Contents/")
		switch {
		case inApp && strings.HasPrefix(p, "/Volumes/") && !core.WithinAny(p, apps): // a home on a second volume
			return "on a disk image or a removable volume"
		case inApp && strings.Contains(p, "/AppTranslocation/"):
			return "a copy macOS runs from a temporary folder"
		case core.WithinAny(p, temp) || strings.Contains(p, "/go-build"):
			return "a temporary build"
		case inApp && !core.WithinAny(p, apps):
			return "in an app outside the Applications folder"
		}
		return ""
	}
	real := resolve(path)
	reason := why(path)
	if reason == "" {
		reason = why(real)
	}
	switch {
	case reason == "":
		return nil
	case strings.Contains(path, ".app/Contents/") || strings.Contains(real, ".app/Contents/"):
		return fail("%s is %s; move acp.app to Applications and open it from there", path, reason)
	}
	return fail("%s is %s; install acp (`curl -fsSL .../install.sh | sh`) or set ACP_BIN to a lasting path", path, reason)
}

// hooksShape reports whether "hooks" is an object and its SessionStart a list (or missing), so a
// rewrite never replaces something acp does not understand.
func hooksShape(data *core.Object) bool {
	v, ok := data.Get("hooks")
	if !ok {
		return true
	}
	hooks, isObj := v.(*core.Object)
	if !isObj {
		return false
	}
	ss, ok := hooks.Get("SessionStart")
	if !ok {
		return true
	}
	_, isList := ss.([]any)
	return isList
}

// hookGroups returns the SessionStart groups of a settings object, creating the containers.
func hookGroups(data *core.Object) []any {
	hooks := data.Object("hooks")
	if hooks == nil {
		hooks = core.NewObject()
		data.Set("hooks", hooks)
	}
	return hooks.Array("SessionStart")
}

// rewriteHooks drops acp's hook entries (all of them, or all but command) and adds command when
// it is missing. It reports whether anything changed.
func rewriteHooks(data *core.Object, command string, install bool) bool {
	groups := hookGroups(data)
	changed, present := false, false
	out := []any{}
	for _, g := range groups {
		group, ok := g.(*core.Object)
		if !ok {
			out = append(out, g)
			continue
		}
		inner := group.Array("hooks")
		kept := []any{}
		for _, h := range inner {
			ho, ok := h.(*core.Object)
			cmd := ""
			if ok {
				cmd = ho.String("command")
			}
			switch {
			// The exact command acp writes counts as its own even when ACP_BIN names the binary
			// something other than acp.
			case !ok || !(core.IsAcpHook(cmd) || (command != "" && cmd == command)):
				kept = append(kept, h)
			case install && cmd == command && !present:
				present = true
				kept = append(kept, h)
			default:
				changed = true
			}
		}
		if len(kept) != len(inner) {
			if len(kept) == 0 {
				continue
			}
			group.Set("hooks", kept)
		}
		out = append(out, group)
	}
	if install && !present {
		out = append(out, core.Obj("matcher", "startup|resume", "hooks", []any{
			core.Obj("type", "command", "command", command, "timeout", 10)}))
		changed = true
	}
	if changed {
		data.Object("hooks").Set("SessionStart", out)
	}
	return changed
}

func hookState(data *core.Object, command string) string {
	state := "not installed"
	for _, g := range hookGroups(data) {
		group, _ := g.(*core.Object)
		for _, h := range group.Array("hooks") {
			ho, _ := h.(*core.Object)
			cmd := ho.String("command")
			switch {
			case cmd != "" && cmd == command:
				return "installed"
			case core.IsLegacyHook(cmd):
				state = "old Python hook (acp hooks install replaces it)"
			case core.IsAcpHook(cmd):
				state = "installed with another path: " + cmd
			}
		}
	}
	return state
}

func cmdHooks(e *Env, a *Args) (int, error) {
	root, err := e.Root()
	if err != nil {
		return 0, err
	}
	action, dry := a.Str("action"), a.Bool("dry-run")
	command := ""
	if bin, err := hookBinary(); err == nil {
		command = core.ShellQuote(bin) + " link --hook"
	} else if action == "install" {
		return 0, err
	}
	if action != "status" && !dry {
		lock, err := lockRoot(e, root)
		if err != nil {
			return 0, err
		}
		defer lock.Release()
	}
	results := []any{}
	failed := false
	for _, t := range core.HookTargets(a.List("settings")) {
		report := func(state string) {
			e.Printf("%s: %s (%s)\n", t.Name, state, t.Path)
			results = append(results, core.Obj("name", t.Name, "path", t.Path, "state", state))
		}
		if !exists(t.Path) {
			report("not found; skipped")
			continue
		}
		data, err := core.ReadObject(t.Path)
		if err != nil {
			failed = true
			report("unreadable, left alone: " + err.Error())
			continue
		}
		if !hooksShape(data) {
			failed = true
			report("\"hooks\" or \"hooks.SessionStart\" has an unexpected shape; left alone")
			continue
		}
		if action == "status" {
			report(hookState(data, command))
			continue
		}
		if !rewriteHooks(data, command, action == "install") {
			report("already " + map[bool]string{true: "installed", false: "absent"}[action == "install"])
			continue
		}
		if dry {
			report("would " + action + " the SessionStart hook")
			continue
		}
		backup, err := backupFile(root, t, time.Now())
		if err == nil {
			err = core.WriteJSON(t.Path, data, 0o600)
		}
		if err != nil {
			failed = true
			report("not changed: " + err.Error())
			continue
		}
		report(action + "ed (backup " + backup + ")")
	}
	e.Set("command", command)
	e.Set("targets", results)
	if failed {
		return 1, nil
	}
	return 0, nil
}

// backupFile copies a settings file into runtime/backups with mode 0600 before acp rewrites it.
func backupFile(root *core.Root, t core.HookTarget, now time.Time) (string, error) {
	data, err := os.ReadFile(t.Path)
	if err != nil {
		return "", err
	}
	name := strings.NewReplacer(" ", "-", "/", "_").Replace(t.Name) + "-" + filepath.Base(t.Path)
	if strings.HasPrefix(t.Name, "settings ") {
		name = "settings-" + filepath.Base(t.Path)
	}
	if err := os.MkdirAll(root.Backups(), 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(root.Backups(), name+"."+now.Format("20060102-150405.000000"))
	return path, os.WriteFile(path, data, 0o600)
}
