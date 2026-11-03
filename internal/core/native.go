package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// AkBin finds the AgentKit CLI: ACP_AK, then PATH, then $AK_INSTALL_DIR/ak or ~/.local/bin/ak.
func AkBin() (string, error) {
	dir := os.Getenv("AK_INSTALL_DIR")
	if dir == "" {
		dir = filepath.Join(HomeDir(), ".local", "bin")
	}
	p, fromEnv := FindCLI("ACP_AK", "ak", dir)
	switch {
	case p != "":
		return p, nil
	case fromEnv:
		return "", fmt.Errorf("ak is not installed (ACP_AK=%s is not an executable file)", os.Getenv("ACP_AK"))
	}
	return "", errors.New("ak is not installed: not on PATH or in ~/.local/bin (see `acp setup`)")
}

// FindCLI finds a command-line program: the file envVar names when it is set (nothing else is
// tried then), else name on PATH, else name in each of dirs in order. It returns "" when none is
// an executable file; fromEnv reports that envVar was set, so a caller can name it in its error.
func FindCLI(envVar, name string, dirs ...string) (path string, fromEnv bool) {
	if p := os.Getenv(envVar); p != "" {
		if IsExecutable(p) {
			return p, true
		}
		return "", true
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, false
	}
	for _, dir := range dirs {
		if p := filepath.Join(dir, name); IsExecutable(p) {
			return p, false
		}
	}
	return "", false
}

// IsExecutable reports whether p is a regular file someone may execute (symlinks followed).
func IsExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// InstallManifest is AgentKit's record of one target's install (empty when missing or unreadable).
func InstallManifest(checkout, target, kit string) *Object {
	o, err := ReadObject(filepath.Join(checkout, ".agentkit", "adapters", target, kit, ".agentkit", "install-manifest.json"))
	if err != nil {
		return NewObject()
	}
	return o
}

func InstalledVersion(m *Object) string { return m.Object("provenance").String("registry_version") }

// MissingKitFiles lists, relative to checkout and sorted, the files one target's install of kit
// recorded in AgentKit's ownership index (native-skill-paths.json) that are gone from disk. The
// install manifest's version still matches then: uninstalling a sibling kit can delete files this
// kit owns (bestagentkits/agentkit#2981). Paths recorded outside the checkout, as after the repo
// moved, are not judged, and a missing or unreadable index lists nothing.
func MissingKitFiles(checkout, target, kit string) []string {
	data, err := os.ReadFile(filepath.Join(checkout, ".agentkit", "adapters", target, kit, ".agentkit", "native-skill-paths.json"))
	if err != nil {
		return nil
	}
	var recorded []string
	if json.Unmarshal(data, &recorded) != nil {
		return nil
	}
	roots := []string{filepath.Clean(checkout)}
	if real, err := filepath.EvalSymlinks(checkout); err == nil && real != roots[0] {
		roots = append(roots, real)
	}
	var out []string
	for _, p := range recorded {
		if !filepath.IsAbs(p) {
			p = filepath.Join(checkout, p)
		}
		p = filepath.Clean(p)
		for _, root := range roots {
			if p == root || !Within(p, root) {
				continue
			}
			if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
				rel, _ := filepath.Rel(root, p)
				out = append(out, filepath.ToSlash(rel))
			}
			break
		}
	}
	sort.Strings(out)
	return out
}

// KitCatalog is every skill of a kit, as recorded by the installs across the fleet.
func (e *Engine) KitCatalog(kit string) []string {
	if e.catalog == nil {
		e.catalog = map[string][]string{}
	}
	if c, ok := e.catalog[kit]; ok {
		return c
	}
	names := map[string]bool{}
	if fleet, err := e.Root.Fleet(); err == nil {
		for _, p := range fleet {
			if p.Repo == "" || !p.UsesKit(kit) {
				continue
			}
			for _, target := range p.AKTargets() {
				for _, s := range InstallManifest(p.Repo, target, kit).Object("skill_selection").Strings("skills") {
					names[s] = true
				}
			}
		}
	}
	out := sortedKeys(names)
	e.catalog[kit] = out
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func setOf(items []string) map[string]bool {
	m := map[string]bool{}
	for _, it := range items {
		m[it] = true
	}
	return m
}

// SelectionMatches reports whether an install recorded a kit's skill selection.
func (e *Engine) SelectionMatches(k KitSpec, manifest *Object) bool {
	recorded := manifest.Object("skill_selection")
	all, list := k.All, k.Skills
	excluded := setOf(k.Exclude)
	if all && len(excluded) == 0 {
		return recorded.String("mode") == "all"
	}
	if !all {
		a, b := recorded.Strings("skills"), append([]string(nil), list...)
		sort.Strings(a)
		sort.Strings(b)
		return strings.Join(a, "\x00") == strings.Join(b, "\x00")
	}
	// ak records an exclusion as an explicit list: it must equal the kit minus the exclusions.
	have := setOf(recorded.Strings("skills"))
	if recorded.String("mode") == "all" {
		return false
	}
	for s := range have {
		if excluded[s] {
			return false
		}
	}
	catalog := e.KitCatalog(k.Name)
	if len(catalog) == 0 {
		return true
	}
	want := map[string]bool{}
	for _, s := range catalog {
		if !excluded[s] {
			want[s] = true
		}
	}
	return strings.Join(sortedKeys(want), "\x00") == strings.Join(sortedKeys(have), "\x00")
}

// StoreLinks are links into the retired runtime/agentkit-store snapshots in a skill folder.
func (e *Engine) StoreLinks(checkout, runtime string) []string {
	folder := filepath.Join(checkout, SkillDirs[runtime])
	if !isRealDir(folder) {
		return nil
	}
	store := filepath.Join(e.Root.Runtime(), "agentkit-store")
	var out []string
	for _, name := range sortedNames(folder) {
		if p := filepath.Join(folder, name); IsManaged(p, []string{store}, "") {
			out = append(out, p)
		}
	}
	return out
}

// SyncNative installs or refreshes every AgentKit kit of a project in a main checkout, each at
// its pinned version. Per target the primary kit goes first, so on Claude Code it owns the files
// the kits share (ak keeps the first owner). On Codex the kits overwrite each other's shared
// agents, the last writer winning (bestagentkits/agentkit#2980), so after an extra kit ran there
// the primary kit is refreshed once more.
func (e *Engine) SyncNative(ctx context.Context, p *Project, main string, log *Log, dry bool) {
	kits := p.Kits()
	if kits == nil {
		return
	}
	for _, bad := range p.BadExtraKits() {
		log.Issue("%s; skipped", bad)
	}
	channel := p.Channel()
	pins := map[string]string{}
	for _, k := range kits {
		if pins[k.Name] = e.Root.PinnedVersion(channel, k.Name); pins[k.Name] == "" {
			log.Issue("no %s/%s pin in agentkit-releases.lock.json; run `acp upgrade`", channel, k.Name)
		}
	}
	ak := &akBin{}
	for _, target := range p.AKTargets() {
		if _, ok := Targets[target]; !ok {
			log.Issue("unknown AgentKit target %q", target)
			continue
		}
		primaryReady, extraRan := false, false
		for _, k := range kits {
			pin := pins[k.Name]
			if pin == "" {
				continue
			}
			changed, ok := e.syncKit(ctx, k, channel, pin, main, target, ak, log, dry)
			if k.Primary {
				primaryReady = ok
			} else if changed {
				extraRan = true
			}
		}
		if target != "codex" || !extraRan || !primaryReady || len(kits) < 2 {
			continue
		}
		primary, pin := kits[0].Name, pins[kits[0].Name]
		log.Change("~ AgentKit %s %s: refresh %s again so its shared agents win", primary, target, pin)
		if dry {
			continue
		}
		if bin := ak.get(log); bin != "" {
			e.runAk(ctx, bin, []string{"kit", "refresh", primary, "--channel", channel, "--version", pin,
				"--target", target, "--yes", "--no-interactive"}, main, target, pin, e.StoreLinks(main, Targets[target]), log)
			e.catalog = nil
		}
	}
}

// syncKit brings one kit of one target to its pin. changed says ak had (or, dry, would have) to
// run; ok says the kit is at its pin afterwards (dry: would be).
func (e *Engine) syncKit(ctx context.Context, k KitSpec, channel, pin, main, target string, ak *akBin, log *Log, dry bool) (changed, ok bool) {
	kit := k.Name
	recorded := InstallManifest(main, target, kit)
	have := InstalledVersion(recorded)
	stale := e.StoreLinks(main, Targets[target])
	same := e.SelectionMatches(k, recorded)
	var missing []string
	if have != "" {
		missing = MissingKitFiles(main, target, kit)
	}
	if have == pin && same && len(stale) == 0 && len(missing) == 0 {
		log.Ok("AgentKit %s %s (%s)", kit, pin, target)
		return false, true
	}
	why := ""
	if !same && recorded.Len() > 0 {
		why = ", new skill selection"
	}
	if len(missing) > 0 {
		// A refresh at the same version writes them back.
		why += fmt.Sprintf(", %d recorded file(s) missing", len(missing))
	}
	label := have
	if label == "" {
		label = "not installed"
		if recorded.Len() > 0 {
			label = "2.x"
		}
	}
	log.Change("~ AgentKit %s %s: %s -> %s%s", kit, target, label, pin, why)
	if dry {
		return true, true
	}
	bin := ak.get(log)
	if bin == "" {
		return true, false
	}
	args := []string{"kit", "refresh", kit}
	if have == "" || !same {
		// Nothing yet, a 2.x install, or a changed selection: start from kit-only output.
		args = []string{"kit", "init", kit}
		if recorded.Len() > 0 {
			args = append(args, "--fresh")
		}
		if !k.All {
			args = append(args, "--skills", strings.Join(k.Skills, ","))
		}
		if len(k.Exclude) > 0 {
			args = append(args, "--exclude-skills", strings.Join(k.Exclude, ","))
		}
	}
	args = append(args, "--channel", channel, "--version", pin, "--target", target, "--yes", "--no-interactive")
	ok = e.runAk(ctx, bin, args, main, target, pin, stale, log)
	e.catalog = nil
	return true, ok
}

// runAk moves aside what ak refuses to write through (retired store links, a symlinked
// .codex/hooks.json), runs ak, and puts them back if ak fails.
// It reports whether ak succeeded and the install manifest shows the pin.
func (e *Engine) runAk(ctx context.Context, bin string, args []string, main, target, pin string, stale []string, log *Log) bool {
	saved := map[string]string{}
	for _, link := range stale {
		if raw, err := os.Readlink(link); err == nil {
			saved[link] = raw
		}
	}
	hooks := filepath.Join(main, ".codex", "hooks.json")
	hooksLink, hooksMade := "", false
	if target == "codex" && lstatIsLink(hooks) {
		hooksLink, _ = os.Readlink(hooks)
	}
	if len(saved) > 0 || hooksLink != "" {
		links := NewObject()
		for _, link := range sortedMapKeys(saved) {
			rel, _ := filepath.Rel(main, link)
			links.Set(rel, saved[link])
		}
		record := Obj("repo", main, "target", target, "links", links)
		if hooksLink != "" {
			record.Set("codex_hooks_link", hooksLink)
		}
		path := filepath.Join(e.Root.Backups(), "store-links-"+e.Now().Format("20060102-150405.000000")+".json")
		if err := WriteJSON(path, record, 0o644); err != nil {
			log.Issue("cannot back up the links ak needs moved (%v); nothing changed", err)
			return false
		}
	}
	for link := range saved {
		os.Remove(link)
	}
	if hooksLink != "" {
		// AgentKit 2.x linked .codex/hooks.json into its old lifecycle root; ak 3 needs a real file.
		content, err := os.ReadFile(hooks)
		os.Remove(hooks)
		if err == nil {
			hooksMade = os.WriteFile(hooks, content, 0o644) == nil
		}
	}
	res := Exec(ctx, ExecOpts{Dir: main, Timeout: AKTimeout()}, bin, args...)
	if res.OK() {
		if InstalledVersion(InstallManifest(main, target, args[2])) != pin {
			log.Issue("%s: ak finished but the install manifest does not show %s", target, pin)
			return false
		}
		return true
	}
	for _, link := range sortedMapKeys(saved) {
		if Exists(link) {
			log.Issue("cannot put back %s -> %s: ak left something there", link, saved[link])
			continue
		}
		os.Symlink(saved[link], link)
	}
	if hooksLink != "" {
		if hooksMade && !lstatIsLink(hooks) {
			os.Remove(hooks)
		}
		if Exists(hooks) {
			log.Issue("cannot put back %s -> %s: ak left something there", hooks, hooksLink)
		} else {
			os.Symlink(hooksLink, hooks)
		}
	}
	reportAkFailure(log, args, "for "+target, res)
	return false
}

// reportAkFailure logs a failed ak run: the first error line (or the last line) as the issue, and
// the command with up to 60 lines of its output as details.
func reportAkFailure(log *Log, args []string, what string, res ExecResult) {
	var detail []string
	for _, line := range strings.Split(res.Stderr+res.Stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			detail = append(detail, line)
		}
	}
	first := "no output"
	for _, line := range detail {
		if strings.Contains(line, "rror") {
			first = strings.TrimSpace(line)
			break
		}
	}
	if first == "no output" && len(detail) > 0 {
		first = strings.TrimSpace(detail[len(detail)-1])
	}
	switch {
	case res.TimedOut:
		log.Issue("`ak kit %s` timed out %s after %s (ACP_AK_TIMEOUT)", args[1], what, AKTimeout())
	case res.Err != nil:
		log.Issue("`ak kit %s` failed %s: %v", args[1], what, res.Err)
	default:
		log.Issue("`ak kit %s` failed %s (exit %d): %s", args[1], what, res.Code, first)
	}
	if len(detail) > 60 {
		detail = detail[len(detail)-60:]
	}
	log.Details = append(log.Details, "$ ak "+strings.Join(args, " "))
	log.Details = append(log.Details, detail...)
}

func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SyncOpts control one project sync.
type SyncOpts struct {
	Dry    bool
	Native bool // run AgentKit (false: --offline)
	Adopt  bool
	// RemoveKits are kits to uninstall before AgentKit syncs (with Native), ones the manifest
	// just dropped.
	RemoveKits []string
}

// SyncProject syncs AgentKit, personal links and every worktree of one project.
func (e *Engine) SyncProject(ctx context.Context, p *Project, o SyncOpts) []*Log {
	if p.Repo == "" || !isDir(p.Repo) {
		log := NewLog(p.ID)
		repo := p.Repo
		if repo == "" {
			repo = "not in projects.local.json"
		}
		log.Issue("repo path is missing: %s", repo)
		return []*Log{log}
	}
	main := resolvePath(p.Repo)
	head := NewLog(p.ID + "  " + main)
	if o.Native {
		if len(o.RemoveKits) > 0 {
			e.RemoveKits(ctx, p, main, o.RemoveKits, head, o.Dry)
		}
		e.SyncNative(ctx, p, main, head, o.Dry)
	}
	if err := e.SyncCheckout(p, main, main, head, LinkOpts{Dry: o.Dry}); err != nil && ctx.Err() != nil {
		return []*Log{head}
	}
	logs := []*Log{head}
	for _, wt := range Worktrees(main) {
		log := NewLog("  worktree " + wt)
		e.SyncCheckout(p, main, wt, log, LinkOpts{Dry: o.Dry, Adopt: o.Adopt})
		logs = append(logs, log)
	}
	return logs
}
