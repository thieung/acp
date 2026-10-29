package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// NewMode is what `acp new` does with a path.
type NewMode string

const (
	ModeCreate NewMode = "create" // a missing folder: create it, git init it
	ModeInit   NewMode = "init"   // an existing folder outside any repo: git init it
	ModeRepo   NewMode = "repo"   // a repo or worktree toplevel: register it like `acp add`
)

// NewPath is a checked `acp new` path. Real is the resolved folder every write uses; Main is the
// main checkout the fleet records (Real unless Real is a worktree).
type NewPath struct {
	Mode       NewMode
	Real, Main string
}

// gitDirVars point git at a repo other than the one around a folder; acp new refuses to run with them.
var gitDirVars = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_INDEX_FILE"}

// classifyEnv makes git's answers comparable: English messages and no ceiling hiding a parent repo.
var classifyEnv = []string{"LC_ALL=C", "LANGUAGE=", "GIT_CEILING_DIRECTORIES="}

// PlanNewPath classifies a path for `acp new` and refuses the ones it must never git init or
// register. It only reads. Containment is checked by file identity as well as by name, so a case
// variant on a case-insensitive disk cannot slip past a protected folder.
func PlanNewPath(root *Root, path string) (NewPath, error) {
	for _, v := range gitDirVars {
		if os.Getenv(v) != "" {
			return NewPath{}, fmt.Errorf("%s is set, so git would act on another repo; unset it and run acp new again", v)
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return NewPath{}, err
	}
	if hasGitPart(abs) {
		return NewPath{}, fmt.Errorf("%s is in a .git folder; pass a project folder", path)
	}
	real, exists, err := resolveNew(abs, path)
	if err != nil {
		return NewPath{}, err
	}
	if hasGitPart(real) {
		return NewPath{}, fmt.Errorf("%s resolves into a .git folder (%s); pass a project folder", path, real)
	}
	home, cp := resolvePath(HomeDir()), resolvePath(root.Dir)
	if err := refuseSpecial(path, real, home, cp); err != nil {
		return NewPath{}, err
	}
	np := NewPath{Real: real, Main: real}
	probe := real
	if !exists {
		probe = filepath.Dir(real)
	}
	top, err := gitContext(probe)
	switch {
	case err != nil:
		return NewPath{}, fmt.Errorf("%s: %v", path, err)
	case top != "" && exists && sameDir(top, real):
		np.Mode = ModeRepo
		main, _ := CheckoutOf(real)
		np.Main = resolvePath(main)
		if err := refuseSpecial(path+"'s main checkout", np.Main, home, cp); err != nil {
			return NewPath{}, err
		}
	case top != "":
		return NewPath{}, fmt.Errorf("%s is inside the git repo %s; pass its top folder or a folder outside it", path, top)
	case exists:
		np.Mode = ModeInit
	default:
		np.Mode = ModeCreate
	}
	if within(cp, real) {
		return NewPath{}, fmt.Errorf("%s contains the control plane %s", path, cp)
	}
	if np.Mode == ModeRepo {
		return np, nil
	}
	for _, h := range GlobalHomes() {
		if h = resolvePath(h); within(real, h) || within(h, real) {
			return NewPath{}, fmt.Errorf("%s is or holds the agent home %s", path, h)
		}
	}
	if !within(real, home) {
		return NewPath{}, fmt.Errorf("%s is outside your home folder %s; acp new only creates or git inits folders inside it", path, home)
	}
	if np.Mode == ModeInit {
		if Exists(filepath.Join(real, ".git")) {
			return NewPath{}, fmt.Errorf("%s has a .git entry git cannot use; fix or remove it first", path)
		}
		for _, name := range sortedNames(real) {
			if Exists(filepath.Join(real, name, ".git")) {
				return NewPath{}, fmt.Errorf("%s contains git repositories (%s); pass a project folder, not a folder of projects", path, name)
			}
		}
	}
	return np, nil
}

func hasGitPart(p string) bool {
	return slices.Contains(strings.Split(p, string(filepath.Separator)), ".git")
}

// sameDir reports whether two paths name one existing folder, whatever their spelling.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(fa, fb)
}

// within is Within by name, or by identity of p or one of its existing ancestors with dir.
func within(p, dir string) bool {
	if Within(p, dir) {
		return true
	}
	d, err := os.Stat(dir)
	if err != nil {
		return false
	}
	for q := p; ; q = filepath.Dir(q) {
		if fi, err := os.Stat(q); err == nil && os.SameFile(fi, d) {
			return true
		}
		if q == filepath.Dir(q) {
			return false
		}
	}
}

// resolveNew resolves an existing folder, or the parent of a missing one plus its name.
func resolveNew(abs, input string) (real string, exists bool, err error) {
	if fi, err := os.Stat(abs); err == nil {
		if !fi.IsDir() {
			return "", false, fmt.Errorf("%s is not a folder", input)
		}
		real, err := filepath.EvalSymlinks(abs)
		return real, true, err
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}
	if _, err := os.Lstat(abs); err == nil {
		return "", false, fmt.Errorf("%s is a link to a missing folder", input)
	}
	parent := filepath.Dir(abs)
	if !isDir(parent) {
		return "", false, fmt.Errorf("the parent folder %s does not exist; acp new creates one folder, not a path", parent)
	}
	rp, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", false, err
	}
	return filepath.Join(rp, filepath.Base(abs)), false, nil
}

func refuseSpecial(input, real, home, cp string) error {
	switch {
	case real == "/":
		return errors.New("refusing to use / as a project")
	case sameDir(real, home):
		return fmt.Errorf("refusing to use your home folder as a project; pass a folder inside it")
	case within(real, cp):
		return fmt.Errorf("%s is inside the control plane %s", input, cp)
	}
	return nil
}

// gitContext returns the work tree toplevel dir is in, "" outside any repo, or an error inside a
// git dir, a bare repo, or anywhere git cannot answer for another reason.
func gitContext(dir string) (string, error) {
	top, err := git(dir, classifyEnv, "rev-parse", "--show-toplevel")
	if err == nil && top != "" {
		return resolvePath(top), nil
	}
	if _, gerr := git(dir, classifyEnv, "rev-parse", "--git-dir"); gerr == nil {
		return "", errors.New("is a git directory or bare repository")
	} else if !strings.Contains(gerr.Error(), "not a git repository (or any of the parent directories)") {
		return "", gerr
	}
	return "", nil
}

// ProjectDefaults is the control plane's file of settings for `acp new`.
func (r *Root) ProjectDefaults() string { return filepath.Join(r.Dir, "project-defaults.json") }

// NewDefaults are project-defaults.json's values; empty fields were not set. File is "" when the
// file does not exist.
type NewDefaults struct {
	Kit, Channel     string
	Runtimes, Skills []string
	File             string
}

// LoadNewDefaults reads project-defaults.json strictly: a typo or a wrong type is an error, not a
// silently ignored value.
func (r *Root) LoadNewDefaults() (NewDefaults, error) {
	var d NewDefaults
	path := r.ProjectDefaults()
	v, err := ReadJSON(path)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	bad := func(format string, a ...any) (NewDefaults, error) {
		return NewDefaults{}, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, a...))
	}
	if err != nil {
		return bad("%v", err)
	}
	o, ok := v.(*Object)
	if !ok {
		return bad("not a JSON object")
	}
	if sv, _ := o.Get("schemaVersion"); fmt.Sprint(sv) != "1" {
		return bad("schemaVersion must be 1")
	}
	for _, k := range o.Keys() {
		val, _ := o.Get(k)
		switch k {
		case "schemaVersion":
		case "agentkit":
			ak, ok := val.(*Object)
			if !ok {
				return bad("agentkit must be an object")
			}
			for _, ak2 := range ak.Keys() {
				s, _ := ak.Get(ak2)
				str, ok := s.(string)
				switch {
				case ak2 != "kit" && ak2 != "channel":
					return bad("unknown key agentkit.%s", ak2)
				case !ok:
					return bad("agentkit.%s must be a string", ak2)
				case ak2 == "kit" && !ValidName(str):
					return bad("invalid kit name %q", str)
				case ak2 == "channel" && str != "stable" && str != "beta":
					return bad("agentkit.channel must be stable or beta")
				}
			}
			d.Kit, d.Channel = ak.String("kit"), ak.String("channel")
		case "runtimes", "skills":
			list, err := stringArray(val)
			if err != nil {
				return bad("%s %v", k, err)
			}
			for _, s := range list {
				if _, known := TargetOf(s); k == "runtimes" && !known {
					return bad("unknown runtime %q (choose from %s)", s, strings.Join(Runtimes, ", "))
				}
				if k == "skills" && !ValidName(s) {
					return bad("invalid skill name %q", s)
				}
			}
			if k == "runtimes" {
				if len(list) == 0 {
					return bad("runtimes is empty")
				}
				d.Runtimes = list
			} else {
				d.Skills = list
			}
		default:
			return bad("unknown key %s", k)
		}
	}
	d.File = path
	return d, nil
}

func stringArray(v any) ([]string, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, errors.New("must be a list of strings")
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, errors.New("must be a list of strings")
		}
		out = append(out, s)
	}
	return out, nil
}

// NewRecord is what `acp new` keeps in <git-dir>/acp-new.json for a folder it git-inited: the agent
// paths that were there before, which are the user's and never hidden, and whether the run finished.
// An unfinished record is what lets a second `acp new` resume.
type NewRecord struct {
	Preexisting []string
	Done        bool
}

func newRecordPath(gitDir string) string { return filepath.Join(gitDir, "acp-new.json") }

func WriteNewRecord(gitDir string, rec *NewRecord) error {
	return WriteJSON(newRecordPath(gitDir), Obj("schemaVersion", 1, "preexisting", AnyList(rec.Preexisting),
		"done", rec.Done), 0o644)
}

// ReadNewRecord returns nil without an error when acp new did not git init this repo.
func ReadNewRecord(gitDir string) (*NewRecord, error) {
	o, err := ReadObject(newRecordPath(gitDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list, err := stringArray(must(o.Get("preexisting")))
	if err != nil {
		return nil, fmt.Errorf("%s: preexisting %v", newRecordPath(gitDir), err)
	}
	return &NewRecord{Preexisting: list, Done: o.Bool("done")}, nil
}

// SnapshotAgentPaths lists the untracked paths under the agent roots of a fresh repo, with every
// parent folder, as git spells them (on macOS git composes Unicode names, so the list must come from
// git to match NewRepoExcludeLines later).
func SnapshotAgentPaths(dir string) ([]string, error) {
	files, err := untrackedAgentPaths(dir)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range files {
		parts := strings.Split(p, "/")
		for i := 1; i <= len(parts); i++ {
			set[strings.Join(parts[:i], "/")] = true
		}
	}
	return sortedKeys(set), nil
}

func untrackedAgentPaths(dir string) ([]string, error) {
	listing, err := GitRead(dir, append([]string{"ls-files", "--others", "-z", "--"}, MirrorRoots...)...)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range strings.Split(listing, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// newContainers hold one entry per skill or plugin; a wholly new entry is hidden as one folder.
var newContainers = []string{".claude/skills", ".agents/skills", ".agents/plugins"}

// NewRepoExcludeLines are the anchored exclude lines for every untracked path under the agent roots
// that was not there before acp new: what AgentKit and acp created. A wholly new .agentkit and a
// wholly new skill or plugin folder take one line each.
func NewRepoExcludeLines(dir string, rec *NewRecord) ([]string, error) {
	files, err := untrackedAgentPaths(dir)
	if err != nil {
		return nil, err
	}
	pre := setOf(rec.Preexisting)
	lines := map[string]bool{}
	for _, p := range files {
		if pre[p] {
			continue
		}
		parts := strings.Split(p, "/")
		line := p
		switch {
		case parts[0] == ".agentkit" && !pre[".agentkit"]:
			line = ".agentkit/"
		case len(parts) > 3 && slices.Contains(newContainers, parts[0]+"/"+parts[1]) && !pre[strings.Join(parts[:3], "/")]:
			line = strings.Join(parts[:3], "/") + "/"
		}
		lines["/"+escapeExclude(line)] = true
	}
	return sortedKeys(lines), nil
}

func escapeExclude(p string) string {
	var b strings.Builder
	for _, r := range p {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	s := b.String()
	if strings.HasSuffix(s, " ") {
		s = strings.TrimSuffix(s, " ") + `\ `
	}
	return s
}

const (
	newBlockStart = "# >>> acp new (local to this clone; acp remove --yes takes it out)"
	newBlockEnd   = "# <<< acp new"
)

// WriteNewRepoExclude puts lines into <git-dir>/info/exclude between acp new's markers, replacing an
// earlier block and keeping everything else.
func WriteNewRepoExclude(gitDir string, lines []string) error {
	path := filepath.Join(gitDir, "info", "exclude")
	kept, err := withoutNewBlock(path)
	if err != nil {
		return err
	}
	if kept != "" && !strings.HasSuffix(kept, "\n") {
		kept += "\n"
	}
	block := newBlockStart + "\n" + strings.Join(append(slices.Clone(lines), newBlockEnd), "\n") + "\n"
	return WriteFileAtomic(path, []byte(kept+block), 0o644)
}

// RemoveNewRepoExclude takes acp new's block out of info/exclude and drops acp-new.json.
func RemoveNewRepoExclude(gitDir string) error {
	path := filepath.Join(gitDir, "info", "exclude")
	if isFile(path) {
		kept, err := withoutNewBlock(path)
		if err != nil {
			return err
		}
		if err := WriteFileAtomic(path, []byte(kept), 0o644); err != nil {
			return err
		}
	}
	if err := os.Remove(newRecordPath(gitDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func withoutNewBlock(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	lines := strings.SplitAfter(string(data), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\n") != newBlockStart {
			out = append(out, lines[i])
			continue
		}
		// Skip to the end marker; without one (an edited file) drop only the start line.
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimRight(lines[j], "\n") == newBlockEnd {
				i = j
				break
			}
		}
	}
	return strings.Join(out, ""), nil
}
