package web

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/thieung/acp/internal/core"
)

// args are the arguments a dashboard action may carry. Each kind reads only the ones it needs,
// after check has validated them.
type args struct {
	ID     string   `json:"id,omitempty"`
	All    bool     `json:"all,omitempty"`
	Path   string   `json:"path,omitempty"`
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
	// Channel and Kit scope an upgrade; empty means acp's defaults.
	Channel string `json:"channel,omitempty"`
	Kit     string `json:"kit,omitempty"`
	// Runtimes and Skills shape a new project; empty means project-defaults.json's or acp's.
	Runtimes []string `json:"runtimes,omitempty"`
	Skills   []string `json:"skills,omitempty"`
}

// kind is one action the dashboard may run. A mutation names the read-only kind that previews it.
type kind struct {
	mutation bool
	preview  string
	needs    string // "id", "id-or-all", "skills", "fleet-path", "repo-path", "new-path", "upgrade" or ""
	argv     func(a args) []string
}

func fixed(argv ...string) func(args) []string { return func(args) []string { return argv } }

func syncArgv(extra ...string) func(args) []string {
	return func(a args) []string {
		target := a.ID
		if a.All {
			target = "--all"
		}
		return append([]string{"sync", target}, extra...)
	}
}

func upgradeArgv(extra ...string) func(args) []string {
	return func(a args) []string {
		argv := append([]string{"upgrade"}, extra...)
		if a.Channel != "" {
			argv = append(argv, "--channel", a.Channel)
		}
		if a.Kit != "" {
			argv = append(argv, "--kit", a.Kit)
		}
		return argv
	}
}

func skillsArgv(extra ...string) func(args) []string {
	return func(a args) []string {
		argv := []string{"skills", "set", a.ID}
		if len(a.Add) > 0 {
			argv = append(argv, "--add", strings.Join(a.Add, ","))
		}
		if len(a.Remove) > 0 {
			argv = append(argv, "--remove", strings.Join(a.Remove, ","))
		}
		return append(argv, extra...)
	}
}

func newArgv(extra ...string) func(args) []string {
	return func(a args) []string {
		argv := []string{"new", a.Path}
		if a.ID != "" {
			argv = append(argv, "--id", a.ID)
		}
		if len(a.Runtimes) > 0 {
			argv = append(argv, "--runtimes", strings.Join(a.Runtimes, ","))
		}
		if len(a.Skills) > 0 {
			argv = append(argv, "--skills", strings.Join(a.Skills, ","))
		}
		return append(argv, extra...)
	}
}

var kinds = map[string]kind{
	"scan": {argv: fixed("scan")},

	"sync.preview": {needs: "id-or-all", argv: syncArgv("--dry-run")},
	"sync":         {mutation: true, preview: "sync.preview", needs: "id-or-all", argv: syncArgv()},

	"adopt.preview": {needs: "id", argv: func(a args) []string { return []string{"sync", a.ID, "--adopt", "--dry-run"} }},
	"adopt":         {mutation: true, preview: "adopt.preview", needs: "id", argv: func(a args) []string { return []string{"sync", a.ID, "--adopt"} }},

	"link.preview": {needs: "fleet-path", argv: func(a args) []string { return []string{"link", a.Path, "--check"} }},
	"link":         {mutation: true, preview: "link.preview", needs: "fleet-path", argv: func(a args) []string { return []string{"link", a.Path} }},

	"skills.preview": {needs: "skills", argv: skillsArgv()},
	"skills.apply":   {mutation: true, preview: "skills.preview", needs: "skills", argv: skillsArgv("--apply")},

	"kit.preview": {needs: "id", argv: func(a args) []string { return []string{"kit", "add", a.ID} }},
	"kit.apply":   {mutation: true, preview: "kit.preview", needs: "id", argv: func(a args) []string { return []string{"kit", "add", a.ID, "--apply"} }},

	"global.preview": {argv: fixed("global", "--check")},
	"global.apply":   {mutation: true, preview: "global.preview", argv: fixed("global")},

	"hooks.install.preview":   {argv: fixed("hooks", "install", "--dry-run")},
	"hooks.install":           {mutation: true, preview: "hooks.install.preview", argv: fixed("hooks", "install")},
	"hooks.uninstall.preview": {argv: fixed("hooks", "uninstall", "--dry-run")},
	"hooks.uninstall":         {mutation: true, preview: "hooks.uninstall.preview", argv: fixed("hooks", "uninstall")},

	"setup.install-ak.preview": {argv: fixed("setup", "install-ak")},
	"setup.install-ak":         {mutation: true, preview: "setup.install-ak.preview", argv: fixed("setup", "install-ak", "--apply")},

	// link-cli links the binary serving the dashboard: inside acp.app, the bundled one.
	"setup.link-cli.preview":    {argv: fixed("setup", "link-cli")},
	"setup.link-cli":            {mutation: true, preview: "setup.link-cli.preview", argv: fixed("setup", "link-cli", "--apply")},
	"setup.install-clt.preview": {argv: fixed("setup", "install-clt")},
	"setup.install-clt":         {mutation: true, preview: "setup.install-clt.preview", argv: fixed("setup", "install-clt", "--apply")},

	// connect registers `acp mcp` with every installed agent CLI; its preview reads config files only.
	"connect.preview": {argv: fixed("connect")},
	"connect.apply":   {mutation: true, preview: "connect.preview", argv: fixed("connect", "--apply")},

	// The apply runs only while a fresh preview still resolves the same versions.
	"upgrade.preview": {needs: "upgrade", argv: upgradeArgv("--dry-run")},
	"upgrade":         {mutation: true, preview: "upgrade.preview", needs: "upgrade", argv: upgradeArgv()},

	"add.preview": {needs: "repo-path", argv: func(a args) []string { return []string{"add", a.Path, "--dry-run"} }},
	"add":         {mutation: true, preview: "add.preview", needs: "repo-path", argv: func(a args) []string { return []string{"add", a.Path} }},

	// new creates folders, so its path goes through the CLI's own rules and must also be inside HOME.
	"new.preview": {needs: "new-path", argv: newArgv("--dry-run")},
	"new":         {mutation: true, preview: "new.preview", needs: "new-path", argv: newArgv()},
}

// check validates the args a kind uses, so nothing reaches acp as a flag or an unexpected path.
func (k kind) check(root *core.Root, a args) error {
	switch k.needs {
	case "id":
		if !core.ValidName(a.ID) {
			return errors.New("invalid project id")
		}
	case "id-or-all":
		if !a.All && !core.ValidName(a.ID) {
			return errors.New("invalid project id")
		}
	case "skills":
		if !core.ValidName(a.ID) {
			return errors.New("invalid project id")
		}
		if len(a.Add)+len(a.Remove) == 0 {
			return errors.New("nothing to add or remove")
		}
		for _, n := range append(append([]string{}, a.Add...), a.Remove...) {
			if !core.ValidName(n) {
				return errors.New("invalid skill name")
			}
		}
	case "upgrade":
		if a.Channel != "" && a.Channel != "stable" && a.Channel != "beta" {
			return errors.New("channel must be stable or beta")
		}
		if a.Kit != "" && !core.ValidName(a.Kit) {
			return errors.New("invalid kit")
		}
	case "fleet-path":
		if !inFleet(root, a.Path) {
			return errors.New("not a checkout or worktree of a fleet project")
		}
	case "new-path":
		if !filepath.IsAbs(a.Path) || filepath.Clean(a.Path) != a.Path {
			return errors.New("path must be absolute")
		}
		np, err := core.PlanNewPath(root, a.Path)
		if err != nil {
			return err
		}
		home, err := filepath.EvalSymlinks(core.HomeDir())
		if err != nil || !core.Within(np.Real, home) || !core.Within(np.Main, home) {
			return errors.New("the dashboard only creates or adds projects inside your home folder")
		}
		if a.ID != "" && !core.ValidName(a.ID) {
			return errors.New("invalid project id")
		}
		for _, r := range a.Runtimes {
			if _, ok := core.TargetOf(r); !ok {
				return errors.New("unknown runtime")
			}
		}
		for _, n := range a.Skills {
			if !core.ValidName(n) {
				return errors.New("invalid skill name")
			}
		}
	case "repo-path":
		if !filepath.IsAbs(a.Path) || filepath.Clean(a.Path) != a.Path {
			return errors.New("path must be absolute")
		}
		if fi, err := os.Stat(a.Path); err != nil || !fi.IsDir() {
			return errors.New("not a folder")
		}
	}
	return nil
}

// inFleet reports whether path is the main checkout or a worktree of a fleet project.
func inFleet(root *core.Root, path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	want, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	fleet, err := root.Fleet()
	if err != nil {
		return false
	}
	for _, p := range fleet {
		main, err := filepath.EvalSymlinks(p.Repo)
		if err != nil {
			continue
		}
		if main == want {
			return true
		}
		for _, wt := range core.Worktrees(main) {
			if real, err := filepath.EvalSymlinks(wt); err == nil && real == want {
				return true
			}
		}
	}
	return false
}
