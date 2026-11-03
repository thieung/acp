// Package scan is acp's read-only health check: it turns machine state into findings with stable
// codes, a plain-language explanation and, when one exists, the acp command that fixes them.
package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/thieung/acp/internal/core"
)

// Severities, most urgent first.
const (
	Error  = "error"
	Warn   = "warn"
	Decide = "decide" // a person has to choose; acp has no safe default
	Info   = "info"
)

// Modules are the dashboard sections findings belong to.
var Modules = []string{"projects", "agentkit", "hooks"}

// Codes is every finding code scan can emit; explain's catalog must cover each one.
var Codes = []string{
	"repo_missing", "kit_not_installed", "kit_outdated", "selection_drift", "personal_skill_missing",
	"personal_link_missing", "real_dir_blocks_link", "foreign_link", "worktree_unlinked",
	"old_copy_shadows_main", "tracked_agent_files_changed", "untracked_acp_links", "exclude_conflict",
	"project_not_tracked",
	"plugin_runtime_unsupported", "plugin_not_installed", "plugin_not_enabled", "plugin_version_drift",
	"plugin_user_scope_enabled",
	"ak_missing", "ak_not_signed_in", "ak_doctor_unhealthy", "pin_missing",
	"hook_target_missing", "hook_duplicate", "acp_hook_missing", "acp_hook_legacy", "acp_hook_stale",
	"mcp_command_missing", "global_skill_drift", "skill_link_broken", "skill_dir_without_skill_md",
	"skill_copy_drift",
	"check_timed_out", "check_failed",
}

// Finding is one thing scan noticed.
type Finding struct {
	ID       string // code + short hash of the subject, stable across runs
	Code     string
	Severity string
	Module   string
	Subject  string
	Params   map[string]string
	Raw      string // acp's own wording, for people who want the detail
	Fix      *Fix
}

// Fix is an existing acp command that resolves a finding.
type Fix struct {
	Kind   string
	Args   []string
	Safety string // safe | backup | confirm
}

// FixKinds maps each fix kind to the `acp describe` command that carries it out.
var FixKinds = map[string]string{
	"link": "link", "sync": "sync", "adopt": "sync", "add": "add", "global": "global",
	"hooks.install": "hooks", "skills.set": "skills set", "kit.add": "kit add",
	"setup.install-ak": "setup install-ak", "upgrade": "upgrade",
}

// argv is the command line without "acp"; preview is the variant that changes nothing.
func (f *Fix) argv(preview bool) []string {
	var cmd []string
	var dry, apply []string
	switch f.Kind {
	case "link":
		cmd, dry = append([]string{"link"}, f.Args...), []string{"--check"}
	case "sync":
		cmd, dry = append([]string{"sync"}, f.Args...), []string{"--dry-run"}
	case "adopt":
		cmd, dry = append(append([]string{"sync"}, f.Args...), "--adopt"), []string{"--dry-run"}
	case "add":
		cmd, dry = append([]string{"add"}, f.Args...), []string{"--dry-run"}
	case "global":
		cmd, dry = []string{"global"}, []string{"--check"}
	case "hooks.install":
		cmd, dry = []string{"hooks", "install"}, []string{"--dry-run"}
	case "skills.set":
		cmd, apply = append([]string{"skills", "set"}, f.Args...), []string{"--apply"}
	case "kit.add":
		cmd, apply = append([]string{"kit", "add"}, f.Args...), []string{"--apply"}
	case "setup.install-ak":
		cmd, apply = []string{"setup", "install-ak"}, []string{"--apply"}
	case "upgrade":
		cmd, dry = append([]string{"upgrade"}, f.Args...), []string{"--dry-run"}
	}
	if preview {
		return append(cmd, dry...)
	}
	return append(cmd, apply...)
}

// Argv and PreviewArgv are the arguments to pass to acp (web and MCP add --json).
func (f *Fix) Argv() []string        { return f.argv(false) }
func (f *Fix) PreviewArgv() []string { return f.argv(true) }

func shell(args []string) string {
	words := []string{"acp"}
	for _, a := range args {
		words = append(words, core.ShellQuote(a))
	}
	return strings.Join(words, " ")
}

func (f *Fix) Command() string { return shell(f.Argv()) }
func (f *Fix) Preview() string { return shell(f.PreviewArgv()) }

// newFinding builds a finding with its id; params and raw text are redacted here, once.
func newFinding(code, severity, module, subject string, params map[string]string, raw string, fix *Fix) Finding {
	sum := sha256.Sum256([]byte(subject))
	clean := map[string]string{}
	for k, v := range params {
		clean[k] = core.Redact(v)
	}
	return Finding{ID: code + "-" + hex.EncodeToString(sum[:4]), Code: code, Severity: severity, Module: module,
		Subject: core.Redact(subject), Params: clean, Raw: core.Redact(raw), Fix: fix}
}

// Object is the finding as JSON, with its explanation rendered.
func (f Finding) Object() *core.Object {
	params := core.NewObject()
	keys := make([]string, 0, len(f.Params))
	for k := range f.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		params.Set(k, f.Params[k])
	}
	var fix any
	if f.Fix != nil {
		fix = core.Obj("kind", f.Fix.Kind, "args", core.AnyList(f.Fix.Args), "safety", f.Fix.Safety,
			"command", f.Fix.Command(), "preview", f.Fix.Preview())
	}
	o := core.Obj("id", f.ID, "code", f.Code, "severity", f.Severity, "module", f.Module, "subject", f.Subject,
		"params", params, "raw", f.Raw, "fix", fix)
	if e, ok := Explain(f.Code, f.Params); ok {
		o.Set("title", e.Title)
		o.Set("what", e.What)
		o.Set("why", e.Why)
		o.Set("ignore", e.Ignore)
		o.Set("todo", e.Todo)
	}
	return o
}
