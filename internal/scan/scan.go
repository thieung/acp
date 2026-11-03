package scan

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thieung/acp/internal/core"
)

// Options narrow a scan.
type Options struct {
	Module string        // "" for every module
	Budget time.Duration // per check; 0 means 20s, several times what a check takes on a busy machine
}

// Report is one scan.
type Report struct {
	ScannedAt time.Time
	Summary   map[string]map[string]int // module -> severity (and "total") -> count
	Findings  []Finding
}

type check struct {
	name, module string
	run          func(ctx context.Context, c *collector)
}

// collector gathers one check's findings.
type collector struct {
	root     *core.Root
	findings []Finding
}

func (c *collector) add(code, severity, module, subject string, params map[string]string, raw string, fix *Fix) {
	c.findings = append(c.findings, newFinding(code, severity, module, subject, params, raw, fix))
}

// Run scans the control plane and this machine. It never writes: git runs with
// GIT_OPTIONAL_LOCKS=0, only regular files are opened, and the only ak calls are `ak whoami` and
// `ak doctor` (which may update ak's own cache under HOME). Each check gets its own time budget; a
// check that runs out is reported as check_timed_out and the scan goes on.
func Run(ctx context.Context, root *core.Root, o Options) *Report {
	if o.Budget <= 0 {
		o.Budget = 20 * time.Second
	}
	var checks []check
	fleet, fleetErr := root.Fleet()
	if fleetErr == nil {
		for _, p := range fleet {
			p := p
			checks = append(checks, check{"project " + p.ID, "projects", func(ctx context.Context, c *collector) {
				projectChecks(ctx, c, p)
			}})
			// Each worktree is its own check, so a project with many worktrees gets a budget per
			// worktree rather than one for all of them.
			if (o.Module != "" && o.Module != "projects") || p.Repo == "" || !isDir(p.Repo) {
				continue
			}
			main := resolve(p.Repo)
			for _, wt := range core.Worktrees(main) {
				wt := wt
				checks = append(checks, check{"worktree " + wt, "projects", func(ctx context.Context, c *collector) {
					checkout(ctx, c, core.NewEngine(c.root), p, main, wt)
				}})
			}
		}
	}
	checks = append(checks,
		check{"activity", "projects", untrackedProjects},
		check{"agentkit", "agentkit", agentkitChecks},
		check{"hooks", "hooks", hookChecks},
		check{"mcp servers", "hooks", mcpChecks},
		check{"user skills", "hooks", skillChecks},
	)
	r := &Report{ScannedAt: time.Now().UTC(), Summary: map[string]map[string]int{}}
	if fleetErr != nil {
		r.Findings = append(r.Findings, newFinding("repo_missing", Error, "projects", "fleet",
			map[string]string{"project": "?", "repo": fleetErr.Error()}, fleetErr.Error(), nil))
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, ch := range checks {
		if o.Module != "" && ch.module != o.Module {
			continue
		}
		wg.Add(1)
		go func(ch check) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			cctx, cancel := context.WithTimeout(ctx, o.Budget)
			defer cancel()
			done := make(chan *collector, 1)
			go func() {
				c := &collector{root: root}
				defer func() {
					if v := recover(); v != nil {
						c.findings = []Finding{newFinding("check_failed", Info, ch.module, ch.name,
							map[string]string{"check": ch.name, "error": fmt.Sprint(v)}, fmt.Sprint(v), nil)}
					}
					done <- c
				}()
				ch.run(cctx, c)
			}()
			var found []Finding
			select {
			case c := <-done:
				// A check that finished only because its commands were stopped has no trustworthy
				// findings; report the timeout instead.
				found = c.findings
				if cctx.Err() != nil {
					found = []Finding{timedOut(ch, o.Budget)}
				}
			case <-cctx.Done():
				found = []Finding{timedOut(ch, o.Budget)}
			}
			mu.Lock()
			r.Findings = append(r.Findings, found...)
			mu.Unlock()
		}(ch)
	}
	wg.Wait()
	// Two checks can report the same subject; the dashboard and MCP address findings by ID.
	seen := map[string]bool{}
	r.Findings = slices.DeleteFunc(r.Findings, func(f Finding) bool {
		dup := seen[f.ID]
		seen[f.ID] = true
		return dup
	})
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Module != b.Module {
			return moduleRank(a.Module) < moduleRank(b.Module)
		}
		if a.Severity != b.Severity {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}
		return a.ID < b.ID
	})
	for _, m := range Modules {
		if o.Module == "" || o.Module == m {
			r.Summary[m] = map[string]int{Error: 0, Warn: 0, Decide: 0, Info: 0, "total": 0}
		}
	}
	for _, f := range r.Findings {
		if s := r.Summary[f.Module]; s != nil {
			s[f.Severity]++
			s["total"]++
		}
	}
	return r
}

func timedOut(ch check, budget time.Duration) Finding {
	return newFinding("check_timed_out", Info, ch.module, ch.name,
		map[string]string{"check": ch.name, "seconds": fmt.Sprint(budget.Seconds())}, "", nil)
}

func moduleRank(m string) int {
	for i, x := range Modules {
		if x == m {
			return i
		}
	}
	return len(Modules)
}

func severityRank(s string) int {
	return map[string]int{Error: 0, Warn: 1, Decide: 2, Info: 3}[s]
}

// Object is the report as JSON.
func (r *Report) Object() *core.Object {
	summary := core.NewObject()
	for _, m := range Modules {
		if s, ok := r.Summary[m]; ok {
			summary.Set(m, core.Obj(Error, s[Error], Warn, s[Warn], Decide, s[Decide], Info, s[Info], "total", s["total"]))
		}
	}
	findings := []any{}
	for _, f := range r.Findings {
		findings = append(findings, f.Object())
	}
	return core.Obj("scanned_at", r.ScannedAt.Format(time.RFC3339), "summary", summary, "findings", findings)
}

// ------------------------------------------------------------------ projects

func projectChecks(ctx context.Context, c *collector, p *core.Project) {
	if p.Repo == "" || !isDir(p.Repo) {
		repo := p.Repo
		if repo == "" {
			repo = "projects.local.json"
		}
		c.add("repo_missing", Error, "projects", p.ID, map[string]string{"project": p.ID, "repo": repo},
			"repo path is missing: "+repo, nil)
		return
	}
	main := resolve(p.Repo)
	eng := core.NewEngine(c.root)
	if p.AK() != nil {
		kit, channel := p.Kit(), p.Channel()
		if pin := c.root.PinnedVersion(channel, kit); pin != "" {
			for _, target := range p.AKTargets() {
				if _, ok := core.Targets[target]; !ok {
					continue
				}
				recorded := core.InstallManifest(main, target, kit)
				have := core.InstalledVersion(recorded)
				params := map[string]string{"project": p.ID, "target": target, "pin": pin, "installed": have}
				subject := p.ID + " " + target
				fix := &Fix{Kind: "sync", Args: []string{p.ID}, Safety: "safe"}
				switch {
				case recorded.Len() == 0:
					c.add("kit_not_installed", Warn, "projects", subject, params, "", fix)
				case have != pin || len(eng.StoreLinks(main, core.Targets[target])) > 0:
					if have == "" {
						params["installed"] = "2.x"
					}
					c.add("kit_outdated", Warn, "projects", subject, params, "", fix)
				case !eng.SelectionMatches(p, recorded):
					c.add("selection_drift", Warn, "projects", subject, params, "", fix)
				}
			}
		}
	}
	pluginChecks(c, p, main)
	if ctx.Err() != nil {
		return
	}
	checkout(ctx, c, eng, p, main, main)
}

// pluginChecks reports what Claude Code records for each plugin the manifest declares, the way
// the Python acp's sync --dry-run and status do. It only reads installed_plugins.json and two
// settings files; the fix is a `claude plugin` command a person runs, never acp.
func pluginChecks(c *collector, p *core.Project, main string) {
	settings := filepath.Join(main, ".claude", "settings.json")
	userSettings := filepath.Join(core.GlobalHomes()["claude"], "settings.json")
	for _, pl := range p.Plugins() {
		subject := p.ID + " " + pl.Name
		params := map[string]string{"project": p.ID, "plugin": pl.Name, "runtime": pl.Runtime, "checkout": main}
		if !pl.Supported() {
			c.add("plugin_runtime_unsupported", Warn, "projects", subject, params,
				fmt.Sprintf("plugin '%s': runtime '%s' is not supported (Claude Code only, named <plugin>@<marketplace>)",
					pl.Name, pl.Runtime), nil)
			continue
		}
		s := core.ReadPluginState(pl, main)
		params["pin"], params["installed"] = pl.Version, s.Installed
		if pl.Version != "" {
			params["pin_text"] = " bản " + pl.Version
		}
		switch {
		case s.Installed == "":
			params["command"] = "claude plugin install " + core.ShellQuote(pl.Name) + " --scope project"
			c.add("plugin_not_installed", Warn, "projects", subject, params, "+ plugin "+pl.Name+" --scope project", nil)
		case !s.Enabled:
			params["settings"], params["command"] = settings, "claude plugin enable "+core.ShellQuote(pl.Name)+" --scope project"
			c.add("plugin_not_enabled", Warn, "projects", subject, params, "+ plugin "+pl.Name+": enable --scope project", nil)
		}
		if s.Installed != "" && pl.Version != "" && s.Installed != pl.Version {
			dp := clone(params)
			dp["command"] = "claude plugin update " + core.ShellQuote(pl.Name)
			c.add("plugin_version_drift", Warn, "projects", subject+" version", dp,
				fmt.Sprintf("~ plugin %s: installed %s, manifest pins %s (`claude plugin update %s`, then move the pin)",
					pl.Name, s.Installed, pl.Version, pl.Name), nil)
		}
		if s.UserEnabled {
			up := clone(params)
			up["settings"], up["user_version"] = userSettings, s.UserVersion
			up["command"] = "claude plugin disable " + core.ShellQuote(pl.Name) + " --scope user"
			c.add("plugin_user_scope_enabled", Warn, "projects", subject+" user scope", up,
				fmt.Sprintf("~ plugin %s is also enabled at user scope: `claude plugin disable %s --scope user`", pl.Name, pl.Name), nil)
		}
	}
}

func clone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// checkout reports what a dry-run link pass and git status say about one checkout.
func checkout(ctx context.Context, c *collector, eng *core.Engine, p *core.Project, main, dir string) {
	log := core.NewLog("")
	eng.SyncCheckout(p, main, dir, log, core.LinkOpts{Dry: true})
	isMain := dir == main
	seen := map[string]bool{}
	conflict := false
	for _, fact := range log.Facts {
		params := map[string]string{"project": p.ID, "checkout": dir, "path": fact.Subject}
		subject := dir + " " + fact.Subject
		if seen[fact.Code+subject] {
			continue
		}
		seen[fact.Code+subject] = true
		switch fact.Code {
		case "personal_skill_missing":
			params["skill"] = fact.Subject
			c.add(fact.Code, Error, "projects", p.ID+" "+fact.Subject, params, "",
				&Fix{Kind: "skills.set", Args: []string{p.ID, "--remove", fact.Subject}, Safety: "confirm"})
		case "real_dir_blocks_link", "foreign_link":
			c.add(fact.Code, Decide, "projects", subject, params, "", nil)
		case "old_copy_shadows_main":
			params["worktree"] = dir
			c.add(fact.Code, Decide, "projects", subject, params, "",
				&Fix{Kind: "adopt", Args: []string{p.ID}, Safety: "backup"})
		case "exclude_conflict":
			conflict = true
			params["reason"] = fact.Subject
			c.add(fact.Code, Decide, "projects", dir, params, fact.Subject, nil)
		}
	}
	// Recording links that are already right only hides them from git status; untracked_acp_links
	// reports that when git actually shows them.
	var changes []string
	for _, ch := range log.Changes {
		if !strings.HasPrefix(ch, "~ hide acp's links") {
			changes = append(changes, ch)
		}
	}
	if n := len(changes); n > 0 {
		params := map[string]string{"project": p.ID, "checkout": dir, "count": fmt.Sprint(n)}
		fix := &Fix{Kind: "link", Args: []string{dir}, Safety: "safe"}
		if isMain {
			c.add("personal_link_missing", Warn, "projects", dir, params, strings.Join(first(changes, 10), "\n"), fix)
		} else {
			params["worktree"] = dir
			c.add("worktree_unlinked", Warn, "projects", dir, params, strings.Join(first(changes, 10), "\n"), fix)
		}
	}
	changed, untracked := gitStatus(ctx, dir)
	if len(changed) > 0 {
		c.add("tracked_agent_files_changed", Decide, "projects", dir,
			map[string]string{"project": p.ID, "checkout": dir, "count": fmt.Sprint(len(changed))},
			strings.Join(first(changed, 10), "\n"), nil)
	}
	if isMain {
		return
	}
	var links []string
	for _, rel := range untracked {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if core.IsManaged(path, []string{main, c.root.Dir}, dir) {
			links = append(links, rel)
		}
	}
	if len(links) > 0 && !conflict {
		c.add("untracked_acp_links", Warn, "projects", dir,
			map[string]string{"project": p.ID, "worktree": dir, "count": fmt.Sprint(len(links))},
			strings.Join(first(links, 10), "\n"), &Fix{Kind: "link", Args: []string{dir}, Safety: "safe"})
	}
}

func first(list []string, n int) []string {
	if len(list) > n {
		return append(list[:n:n], fmt.Sprintf("... %d more", len(list)-n))
	}
	return list
}

// gitStatus lists tracked agent files that are deleted, modified or changed type, and untracked
// symlinks, under the agent roots of one checkout. It never takes git's index lock.
func gitStatus(ctx context.Context, dir string) (changed, untrackedLinks []string) {
	args := append(append(slices.Clone(core.GitNoMonitor), "-C", dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--"), core.MirrorRoots...)
	res := core.Exec(ctx, core.ExecOpts{Timeout: core.GitTimeout(), Env: []string{"GIT_OPTIONAL_LOCKS=0"}}, "git", args...)
	if !res.OK() {
		return nil, nil
	}
	entries := strings.Split(res.Stdout, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		xy, path := e[:2], e[3:]
		if xy[0] == 'R' || xy[0] == 'C' {
			i++ // the source path follows
		}
		switch {
		case xy == "??":
			if fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path))); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				untrackedLinks = append(untrackedLinks, path)
			}
		case strings.ContainsAny(xy, "DMT"):
			changed = append(changed, xy+" "+path)
		}
	}
	return changed, untrackedLinks
}

// untrackedProjects reports repos with at least 2 sessions in 30 days that are not in the fleet.
func untrackedProjects(ctx context.Context, c *collector) {
	rows, _, err := core.Activity(c.root, 30)
	if err != nil {
		return
	}
	for _, r := range rows {
		if r.Project != "" || r.Claude+r.Codex < 2 {
			continue
		}
		c.add("project_not_tracked", Info, "projects", r.Repo,
			map[string]string{"repo": r.Repo, "sessions": fmt.Sprint(r.Claude + r.Codex)}, "",
			&Fix{Kind: "add", Args: []string{r.Repo}, Safety: "safe"})
	}
}

// ------------------------------------------------------------------ agentkit

func agentkitChecks(ctx context.Context, c *collector) {
	fleet, err := c.root.Fleet()
	if err != nil {
		return
	}
	pins := map[string]bool{}
	for _, p := range fleet {
		if p.AK() == nil {
			continue
		}
		key := p.Channel() + "/" + p.Kit()
		if pins[key] {
			continue
		}
		pins[key] = true
		if c.root.PinnedVersion(p.Channel(), p.Kit()) == "" {
			c.add("pin_missing", Error, "agentkit", key, map[string]string{"channel": p.Channel(), "kit": p.Kit()}, "",
				&Fix{Kind: "upgrade", Args: []string{"--channel", p.Channel(), "--kit", p.Kit()}, Safety: "confirm"})
		}
	}
	if len(pins) == 0 {
		return
	}
	bin, err := core.AkBin()
	if err != nil {
		c.add("ak_missing", Error, "agentkit", "ak", nil, err.Error(), &Fix{Kind: "setup.install-ak", Safety: "confirm"})
		return
	}
	if !akData(ctx, bin, "whoami").Bool("authenticated") {
		c.add("ak_not_signed_in", Warn, "agentkit", "ak", nil, "", nil)
	}
	if ctx.Err() != nil {
		return
	}
	if !akData(ctx, bin, "doctor").Bool("healthy") {
		c.add("ak_doctor_unhealthy", Warn, "agentkit", "ak", nil, "", nil)
	}
}

func akData(ctx context.Context, bin, sub string) *core.Object {
	res := core.Exec(ctx, core.ExecOpts{Timeout: 2 * time.Minute}, bin, sub, "--json")
	v, err := core.ParseJSON([]byte(res.Stdout))
	if err != nil {
		return core.NewObject()
	}
	if o, ok := v.(*core.Object); ok && o.Object("data") != nil {
		return o.Object("data")
	}
	return core.NewObject()
}

// --------------------------------------------------------------------- hooks

type hookEntry struct{ event, matcher, command string }

// hookEntries reads every hook command of a settings file.
func hookEntries(data *core.Object) []hookEntry {
	var out []hookEntry
	hooks := data.Object("hooks")
	if hooks == nil {
		return nil
	}
	for _, event := range hooks.Keys() {
		for _, g := range hooks.Array(event) {
			group, ok := g.(*core.Object)
			if !ok {
				continue
			}
			for _, h := range group.Array("hooks") {
				if ho, ok := h.(*core.Object); ok && ho.String("command") != "" {
					out = append(out, hookEntry{event, group.String("matcher"), ho.String("command")})
				}
			}
		}
	}
	return out
}

func hookChecks(ctx context.Context, c *collector) {
	for _, t := range core.HookTargets(nil) {
		if ctx.Err() != nil {
			return
		}
		data, ok := readSettings(t.Path)
		if !ok {
			continue
		}
		entries := hookEntries(data)
		hasAcp, legacy := false, false
		var stale []string
		groups := map[string][]hookEntry{}
		for _, h := range entries {
			if core.IsLegacyHook(h.command) {
				legacy = true
				continue
			}
			if bin := core.HookBinary(h.command); bin != "" {
				if !commandExists(bin) {
					stale = append(stale, bin)
				} else {
					hasAcp = true
				}
				continue
			}
			key := h.event + "\x00" + h.matcher
			groups[key] = append(groups[key], h)
			if target := scriptOf(h.command); target != "" {
				if _, err := os.Lstat(target); err != nil {
					c.add("hook_target_missing", Warn, "hooks", t.Name+": "+target,
						map[string]string{"file": t.Path, "event": h.event, "target": target}, h.command, nil)
				}
			}
		}
		install := &Fix{Kind: "hooks.install", Safety: "backup"}
		switch {
		case len(stale) > 0:
			c.add("acp_hook_stale", Error, "hooks", t.Path, map[string]string{"file": t.Path, "binary": stale[0]}, "", install)
		case legacy && !hasAcp:
			c.add("acp_hook_legacy", Warn, "hooks", t.Path, map[string]string{"file": t.Path}, "", install)
		case !hasAcp:
			c.add("acp_hook_missing", Warn, "hooks", t.Path, map[string]string{"file": t.Path}, "", install)
		}
		keys := make([]string, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			duplicates(c, t, groups[k])
		}
	}
}

// duplicates reports hooks of one event and matcher that run the same command, or two scripts
// with identical content.
func duplicates(c *collector, t core.HookTarget, hooks []hookEntry) {
	byCommand := map[string]int{}
	byContent := map[string]string{}
	reported := map[string]bool{}
	for _, h := range hooks {
		byCommand[h.command]++
		if byCommand[h.command] == 2 {
			reported[h.command] = true
			c.add("hook_duplicate", Info, "hooks", t.Name+" "+h.event+" "+h.command,
				map[string]string{"file": t.Path, "event": h.event, "command": h.command}, "", nil)
			continue
		}
		if byCommand[h.command] > 1 {
			continue
		}
		target := scriptOf(h.command)
		sum := hashSmallFile(target)
		if sum == "" {
			continue
		}
		if other, ok := byContent[sum]; ok && !reported[other] {
			reported[other] = true
			c.add("hook_duplicate", Info, "hooks", t.Name+" "+h.event+" "+h.command,
				map[string]string{"file": t.Path, "event": h.event, "command": h.command}, other, nil)
			continue
		}
		byContent[sum] = h.command
	}
}

// readSettings opens a settings file only when it is a regular file.
func readSettings(path string) (*core.Object, bool) {
	fi, err := os.Lstat(path)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		fi, err = os.Stat(path)
	}
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	data, err := core.ReadObject(path)
	return data, err == nil
}

var interpreters = map[string]bool{"sh": true, "bash": true, "zsh": true, "fish": true, "python": true,
	"python3": true, "node": true, "bun": true, "deno": true, "ruby": true, "perl": true}

// scriptOf returns the absolute path a hook command runs: its first word, or the first argument of
// an interpreter. Paths with other variables are skipped; ~ and $HOME are expanded.
func scriptOf(command string) string {
	words := splitWords(command)
	if len(words) == 0 {
		return ""
	}
	cand := words[0]
	if interpreters[filepath.Base(cand)] {
		cand = ""
		for _, w := range words[1:] {
			if !strings.HasPrefix(w, "-") {
				cand = w
				break
			}
		}
	}
	home := core.HomeDir()
	switch {
	case strings.HasPrefix(cand, "~/"):
		cand = filepath.Join(home, cand[2:])
	case strings.HasPrefix(cand, "$HOME/"):
		cand = filepath.Join(home, cand[6:])
	case strings.HasPrefix(cand, "${HOME}/"):
		cand = filepath.Join(home, cand[8:])
	}
	if !filepath.IsAbs(cand) || strings.ContainsAny(cand, "$`*?") {
		return ""
	}
	return filepath.Clean(cand)
}

// splitWords splits a command on spaces, honouring single and double quotes.
func splitWords(s string) []string {
	var out []string
	var cur strings.Builder
	in, quoted := rune(0), false
	for _, r := range s {
		switch {
		case in != 0 && r == in:
			in = 0
		case in != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			in, quoted = r, true
		case r == ' ' || r == '\t':
			if cur.Len() > 0 || quoted {
				out = append(out, cur.String())
				cur.Reset()
				quoted = false
			}
		case r == ';' || r == '|' || r == '&':
			if cur.Len() > 0 {
				out = append(out, cur.String())
			}
			return out
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 || quoted {
		out = append(out, cur.String())
	}
	return out
}

// hashSmallFile hashes a regular file of at most 1 MiB; anything else gives "".
func hashSmallFile(path string) string {
	if path == "" {
		return ""
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, 1<<20)); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func commandExists(cmd string) bool {
	if filepath.IsAbs(cmd) {
		fi, err := os.Stat(cmd)
		return err == nil && !fi.IsDir()
	}
	if strings.Contains(cmd, "/") {
		return true // relative to wherever the hook runs; nothing to check
	}
	_, err := exec.LookPath(cmd)
	return err == nil
}

// ----------------------------------------------------------------- mcp

// MCPServer is one MCP server from a Claude Code or Codex config. Only its name and command leave
// acp; args, env, headers and urls can hold secrets. Args are read only so `acp connect` can tell
// whether its own entry is current; they never reach a finding, an envelope or a log.
type MCPServer struct {
	Source, File, Name, Command string
	Agent                       string // "claude" or "codex"
	Scope                       string // "user" (every project) or "project"
	Args                        []string
	Remote                      bool
}

// mcpChecks reports stdio MCP servers whose command is missing. Only the server name and command
// path are read into a finding; env, headers and args never are.
func mcpChecks(ctx context.Context, c *collector) {
	for _, s := range MCPServers() {
		if !s.Remote && s.Command != "" && !commandExists(s.Command) {
			c.add("mcp_command_missing", Warn, "hooks", s.Source+": "+s.Name,
				map[string]string{"source": s.Source, "server": s.Name, "command": s.Command, "file": s.File}, "", nil)
		}
	}
}

// MCPServers lists the servers of ~/.claude.json (user and per project) and Codex's config.toml.
func MCPServers() []MCPServer {
	var out []MCPServer
	claude := filepath.Join(core.HomeDir(), ".claude.json")
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		claude = filepath.Join(dir, ".claude.json")
	}
	if data, ok := readSettings(claude); ok {
		out = append(out, claudeServers(claude, "~/.claude.json", "user", data.Object("mcpServers"))...)
		projects := data.Object("projects")
		for _, k := range projects.Keys() {
			out = append(out, claudeServers(claude, "~/.claude.json project "+k, "project",
				projects.Object(k).Object("mcpServers"))...)
		}
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(core.HomeDir(), ".codex")
	}
	return append(out, codexServers(filepath.Join(codexHome, "config.toml"))...)
}

func claudeServers(file, source, scope string, servers *core.Object) []MCPServer {
	var out []MCPServer
	for _, name := range servers.Keys() {
		s := servers.Object(name)
		if s == nil {
			continue
		}
		remote := s.Has("url") || (s.String("type") != "" && s.String("type") != "stdio")
		srv := MCPServer{Source: source, File: file, Name: name, Agent: "claude", Scope: scope, Remote: remote}
		if !remote {
			srv.Command = s.String("command")
			for _, a := range s.Array("args") {
				srv.Args = append(srv.Args, fmt.Sprint(a))
			}
		}
		out = append(out, srv)
	}
	return out
}

// codexServers reads [mcp_servers.NAME] sections of a Codex config.toml line by line. Codex has
// no project scope for MCP servers, so every one is a user-scope server.
func codexServers(path string) []MCPServer {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4<<20 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	type server struct {
		command, url string
		args         []string
	}
	servers := map[string]*server{}
	var order []string
	var cur *server
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			cur = nil
			header := strings.Trim(line, "[] ")
			if !strings.HasPrefix(header, "mcp_servers.") {
				continue
			}
			rest := strings.TrimPrefix(header, "mcp_servers.")
			var name string
			if strings.HasPrefix(rest, `"`) {
				if end := strings.Index(rest[1:], `"`); end >= 0 {
					name, rest = rest[1:end+1], rest[end+2:]
				}
			} else if i := strings.Index(rest, "."); i >= 0 {
				name, rest = rest[:i], rest[i:]
			} else {
				name, rest = rest, ""
			}
			if rest != "" || name == "" {
				continue // a sub-table such as [mcp_servers.x.env]
			}
			if servers[name] == nil {
				servers[name] = &server{}
				order = append(order, name)
			}
			cur = servers[name]
			continue
		}
		if cur == nil {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "command":
			cur.command = tomlString(value)
		case "url":
			cur.url = tomlString(value)
		case "args":
			// An array may run over several lines.
			args, done := tomlStrings(value)
			for !done && sc.Scan() {
				value += "\n" + sc.Text()
				args, done = tomlStrings(value)
			}
			cur.args = args
		}
	}
	var out []MCPServer
	for _, name := range order {
		s := servers[name]
		srv := MCPServer{Source: "~/.codex/config.toml", File: path, Name: name, Agent: "codex", Scope: "user",
			Remote: s.url != ""}
		if !srv.Remote {
			srv.Command, srv.Args = s.command, s.args
		}
		out = append(out, srv)
	}
	return out
}

func tomlString(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : end+1]
		}
	}
	return ""
}

// tomlStrings reads a TOML array of strings; done is false until its closing bracket. Basic strings
// keep only the \" and \\ escapes, which is all a path or a flag needs.
func tomlStrings(v string) (out []string, done bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") {
		return nil, true
	}
	for i := 1; i < len(v); i++ {
		switch c := v[i]; c {
		case ']':
			return out, true
		case '#':
			for i < len(v) && v[i] != '\n' {
				i++
			}
		case '"', '\'':
			var b strings.Builder
			j := i + 1
			for ; j < len(v) && v[j] != c; j++ {
				if c == '"' && v[j] == '\\' && j+1 < len(v) {
					j++
				}
				b.WriteByte(v[j])
			}
			out = append(out, b.String())
			i = j
		}
	}
	return out, false
}

// ----------------------------------------------------------------- skills

func skillChecks(ctx context.Context, c *collector) {
	if log, err := core.GlobalSync(c.root, true); err == nil && len(log.Changes)+len(log.Issues) > 0 {
		lines := append(append([]string{}, log.Changes...), log.Issues...)
		c.add("global_skill_drift", Warn, "hooks", "global-skills.json",
			map[string]string{"count": fmt.Sprint(len(lines))}, strings.Join(first(lines, 10), "\n"),
			&Fix{Kind: "global", Safety: "safe"})
	}
	copies := map[string][]string{} // skill -> home folders with a real copy
	for _, h := range core.MergedHomes(nil) {
		if ctx.Err() != nil {
			return
		}
		entries, err := os.ReadDir(h.Folder)
		if err != nil {
			continue
		}
		for _, en := range entries {
			name := en.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			p := filepath.Join(h.Folder, name)
			fi, err := os.Lstat(p)
			if err != nil {
				continue
			}
			params := map[string]string{"home": h.Folder, "skill": name}
			switch {
			case fi.Mode()&os.ModeSymlink != 0:
				if _, err := os.Stat(p); err != nil {
					var fix *Fix
					if core.IsManaged(p, []string{c.root.Skills()}, "") {
						fix = &Fix{Kind: "global", Safety: "safe"}
					}
					dest, _ := os.Readlink(p)
					c.add("skill_link_broken", Warn, "hooks", p, params, name+" -> "+dest, fix)
				}
			case fi.IsDir():
				if _, err := os.Lstat(filepath.Join(p, "SKILL.md")); err != nil {
					c.add("skill_dir_without_skill_md", Info, "hooks", p, params, "", nil)
					continue
				}
				copies[name] = append(copies[name], p)
			}
		}
	}
	names := make([]string, 0, len(copies))
	for n := range copies {
		names = append(names, n)
	}
	sort.Strings(names)
	// One stale runtime usually differs on every skill it holds, so drift is reported once per set
	// of homes rather than once per skill.
	var groups []string
	drifted := map[string][]string{} // homes -> skills
	for _, name := range names {
		dirs := copies[name]
		if len(dirs) < 2 {
			continue
		}
		want := treeHash(dirs[0])
		if want == "" {
			continue
		}
		for _, d := range dirs[1:] {
			if ctx.Err() != nil {
				return
			}
			if got := treeHash(d); got != "" && got != want {
				homes := make([]string, len(dirs))
				for i, d := range dirs {
					homes[i] = filepath.Dir(d)
				}
				key := strings.Join(homes, ", ")
				if _, ok := drifted[key]; !ok {
					groups = append(groups, key)
				}
				drifted[key] = append(drifted[key], name)
				break
			}
		}
	}
	for _, homes := range groups {
		skills := drifted[homes]
		c.add("skill_copy_drift", Info, "hooks", homes,
			map[string]string{"homes": homes, "count": fmt.Sprint(len(skills)), "skills": strings.Join(first(skills, 10), ", ")},
			strings.Join(skills, "\n"), nil)
	}
}

// treeHash hashes the names and contents of the regular files (at most 1 MiB each) in a skill
// folder. It gives "" for a folder too big to compare (more than 200 files), and skips .git and
// node_modules.
func treeHash(dir string) string {
	h := sha256.New()
	files := 0
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && (d.Name() == ".git" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if files++; files > 200 {
			return io.EOF
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s\x00%s\x00", rel, hashSmallFile(p))
		return nil
	})
	if err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

func resolve(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}
