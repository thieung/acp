// Package testkit builds disposable control planes, repos and a fake `ak` for acp's tests.
package testkit

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// FakeAK stands in for the AgentKit CLI. `kit init|refresh` records the call in .ak-calls, writes
// ak-cook (and .claude/agents for claude-code), an install manifest and, like ak, the absolute
// paths of the files it wrote in native-skill-paths.json; it fails like ak when
// .agentkit/BROKEN exists, when .codex/hooks.json is a symlink or when ak-cook is a symlink.
// `changelog` reports 9.9.9-beta.1. `login` records its argv and the names (never the values) of
// key-like env vars, reads the key from stdin and keeps only its sha256 in $HOME/.ak-login; a key
// containing "bad" fails like ak, echoing the key to stderr so tests prove acp does not pass it
// on. `whoami` reports that sign-in in the shape of `ak whoami --json`.
const FakeAK = `#!/bin/sh
if [ "$1" = login ]; then
  echo "login $(echo "$@" | cut -d' ' -f2-) $(env | cut -d= -f1 | grep -E 'AGENTKIT|API_KEY' | tr '\n' ' ')" >> .ak-calls
  key=$(cat)
  case "$key" in *bad*)
    echo '{"error":"invalid key <stdin>"}'; echo "invalid key $key" >&2; exit 1 ;;
  esac
  if command -v sha256sum >/dev/null; then sum=$(printf %s "$key" | sha256sum); else sum=$(printf %s "$key" | shasum -a 256); fi
  echo "${sum%% *}" > "$HOME/.ak-login"
  exit 0
fi
if [ "$1" = whoami ]; then
  if [ -s "$HOME/.ak-login" ]; then auth=true; method='"api_key"'; else auth=false; method=null; fi
  echo '{"schema_version":1,"kind":"whoami","data":{"authenticated":'$auth',"authMethod":'$method',"licenses":[]}}'
  exit 0
fi
if [ "$1" = kit ] && [ -e .agentkit/BROKEN ]; then
  echo "error: install blocked by .agentkit/BROKEN" >&2; exit 1
fi
if [ "$1" = kit ] && { [ "$2" = init ] || [ "$2" = refresh ]; }; then
  sub=$2; kit=$3; shift 3
  target=; version=; fresh=; selection='{"mode":"all"}'
  while [ $# -gt 0 ]; do
    case "$1" in
      --target) target=$2; shift ;;
      --version) version=$2; shift ;;
      --fresh) fresh=" --fresh" ;;
      --exclude-skills) selection='{"mode":"list","skills":[]}'; shift ;;
      --skills|--channel) shift ;;
    esac
    shift
  done
  echo "kit $sub $kit$fresh" >> .ak-calls
  if [ "$target" = codex ] && [ -L .codex/hooks.json ]; then
    echo "hook journal: .codex/hooks.json is not a regular file" >&2; exit 1
  fi
  skills=.agents/skills
  [ "$target" = claude-code ] && skills=.claude/skills
  if [ -L "$skills/ak-cook" ]; then echo "anchor is not a real directory: $skills/ak-cook" >&2; exit 1; fi
  mkdir -p "$skills/ak-cook"
  printf -- '---\nname: ak-cook\n---\n' > "$skills/ak-cook/SKILL.md"
  owned="\"$(pwd -P)/$skills/ak-cook/SKILL.md\""
  if [ "$target" = claude-code ]; then
    mkdir -p .claude/agents; printf planner > .claude/agents/planner.md
    owned="$owned, \"$(pwd -P)/.claude/agents/planner.md\""
  fi
  meta=".agentkit/adapters/$target/$kit/.agentkit"
  mkdir -p "$meta"
  printf '{"provenance": {"registry_version": "%s"}, "skill_selection": %s}' "$version" "$selection" > "$meta/install-manifest.json"
  printf '[%s]\n' "$owned" > "$meta/native-skill-paths.json"
  exit 0
fi
if [ "$1" = changelog ]; then
  echo '{"data": {"releases": [{"version": "9.9.9-beta.1", "components": [{"target": "kit-engineer"}]}]}}'
  exit 0
fi
exit 0
`

// FakeClaude stands in for the Claude Code CLI. `mcp add` and `mcp remove` append their argv to
// $HOME/.fake-claude-calls and change the user-scope mcpServers of ~/.claude.json like claude does
// (add refuses a name that exists), so acp reads the result from the real file. Any other call is
// recorded as UNEXPECTED and fails. The JSON is rewritten with perl's core JSON::PP.
const FakeClaude = `#!/bin/sh
calls="$HOME/.fake-claude-calls"
case "$1 $2" in
  "mcp add"|"mcp remove") echo "$*" >> "$calls" ;;
  *) echo "UNEXPECTED $*" >> "$calls"; exit 1 ;;
esac
op=$2; shift 2
scope=local; name=
while [ $# -gt 0 ]; do
  case "$1" in
    --scope|-s) scope=$2; shift 2 ;;
    --) shift; break ;;
    *) if [ -z "$name" ]; then name=$1; shift; else break; fi ;;
  esac
done
exec perl -MJSON::PP -e '
my ($file, $op, $scope, $name, @cmd) = @ARGV;
die "fake claude: only --scope user\n" if $scope ne "user";
my $data = {};
if (-e $file) { local $/; open(my $in, "<", $file) or die; $data = decode_json(<$in>); }
my $servers = $data->{mcpServers} ||= {};
if ($op eq "add") {
  die "MCP server $name already exists in user config\n" if exists $servers->{$name};
  $servers->{$name} = {type => "stdio", command => $cmd[0], args => [@cmd[1..$#cmd]], env => {}};
} else {
  die "No user-scoped MCP server found with name: $name\n" unless delete $servers->{$name};
}
open(my $out, ">", $file) or die; print $out JSON::PP->new->pretty->encode($data);
' "${CLAUDE_CONFIG_DIR:-$HOME}/.claude.json" "$op" "$scope" "$name" "$@"
`

// FakeCodex stands in for the Codex CLI: `mcp add NAME -- CMD ARGS...` appends a [mcp_servers.NAME]
// table to ~/.codex/config.toml and `mcp remove NAME` drops it, each recording its argv in
// $HOME/.fake-codex-calls. Any other call is recorded as UNEXPECTED and fails.
const FakeCodex = `#!/bin/sh
calls="$HOME/.fake-codex-calls"
case "$1 $2" in
  "mcp add"|"mcp remove") echo "$*" >> "$calls" ;;
  *) echo "UNEXPECTED $*" >> "$calls"; exit 1 ;;
esac
config="${CODEX_HOME:-$HOME/.codex}/config.toml"
op=$2; name=$3; shift 3
has() { [ -f "$config" ] && grep -qx "\[mcp_servers\.$name\]" "$config"; }
if [ "$op" = add ]; then
  if has; then echo "MCP server $name already exists" >&2; exit 1; fi
  [ "$1" = -- ] && shift
  command=$1; shift
  list=
  for a in "$@"; do list="$list${list:+, }\"$a\""; done
  printf '\n[mcp_servers.%s]\ncommand = "%s"\nargs = [%s]\n' "$name" "$command" "$list" >> "$config"
  exit 0
fi
if ! has; then echo "No MCP server named $name" >&2; exit 1; fi
awk -v t="[mcp_servers.$name]" -v sub_="[mcp_servers.$name." '
  /^\[/ { drop = ($0 == t || index($0, sub_) == 1) }
  !drop' "$config" > "$config.tmp" && mv "$config.tmp" "$config"
`

// Fixture is a control plane with one project "demo": a git repo with a tracked .claude/rules
// file, an untracked settings.local.json and one worktree. Git runs with a private global config
// whose excludes file ignores plans/; HOME, ACP_ROOT and ACP_AK point into the fixture, and
// ACP_CLAUDE and ACP_CODEX at the fake agent CLIs.
type Fixture struct {
	T                                           *testing.T
	Base, CP, Repo, WT, Home, AK, Claude, Codex string
}

func New(t *testing.T) *Fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &Fixture{T: t, Base: base, CP: filepath.Join(base, "cp"), Repo: filepath.Join(base, "repo"),
		WT: filepath.Join(base, "wt"), Home: filepath.Join(base, "home"), AK: filepath.Join(base, "bin/ak"),
		Claude: filepath.Join(base, "bin/claude"), Codex: filepath.Join(base, "bin/codex")}
	ignore := filepath.Join(base, "global-ignore")
	f.Write(ignore, "plans/\n")
	f.Write(filepath.Join(base, "gitconfig"), "[user]\n\tname = t\n\temail = t@t\n[init]\n\tdefaultBranch = main\n"+
		"[core]\n\texcludesFile = "+ignore+"\n")
	f.Executable(f.AK, FakeAK)
	f.Executable(f.Claude, FakeClaude)
	f.Executable(f.Codex, FakeCodex)
	for k, v := range map[string]string{"HOME": f.Home, "GIT_CONFIG_GLOBAL": filepath.Join(base, "gitconfig"),
		"GIT_CONFIG_NOSYSTEM": "1", "XDG_CONFIG_HOME": filepath.Join(base, "xdg"), "ACP_ROOT": f.CP, "ACP_AK": f.AK,
		"ACP_CLAUDE": f.Claude, "ACP_CODEX": f.Codex,
		"CLAUDE_CONFIG_DIR": "", "CODEX_HOME": "", "PI_CODING_AGENT_DIR": "", "OMP_HOME": "", "ACP_BIN": ""} {
		t.Setenv(k, v)
	}
	os.MkdirAll(filepath.Join(f.Home, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(f.Home, ".codex"), 0o755)

	f.Write(filepath.Join(f.CP, "skills/herdr/SKILL.md"), "---\nname: herdr\n---\n")
	f.Write(filepath.Join(f.CP, "agentkit-releases.lock.json"),
		`{"schemaVersion": 2, "channels": {"beta": {"engineer": "3.0.0-beta.36"}}}`)
	f.Manifest("demo", `{"id": "demo", "skills": [{"name": "herdr", "runtimes": ["claude", "codex"]}],
  "agentkit": {"kit": "engineer", "channel": "beta", "targets": ["claude-code", "codex"], "skills": "all"}}`)
	f.Overlay(map[string]string{"demo": f.Repo})

	os.MkdirAll(f.Repo, 0o755)
	f.Git(f.Repo, "init", "-q")
	f.Write(filepath.Join(f.Repo, ".claude/rules/team.md"), "tracked")
	f.Git(f.Repo, "add", ".")
	f.Git(f.Repo, "commit", "-qm", "init")
	f.Write(filepath.Join(f.Repo, ".claude/settings.local.json"), "{}")
	f.Git(f.Repo, "worktree", "add", "-q", "-b", "feature", f.WT)
	return f
}

func (f *Fixture) Write(path, content string) {
	f.T.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.T.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.T.Fatal(err)
	}
}

func (f *Fixture) Executable(path, content string) {
	f.Write(path, content)
	os.Chmod(path, 0o755)
}

func (f *Fixture) Read(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func (f *Fixture) Manifest(id, json string) {
	f.Write(filepath.Join(f.CP, "projects", id+".json"), json)
}

func (f *Fixture) Overlay(repos map[string]string) {
	s := `{"schemaVersion": 1, "repos": {`
	first := true
	for k, v := range repos {
		if !first {
			s += ", "
		}
		first = false
		s += `"` + k + `": "` + v + `"`
	}
	f.Write(filepath.Join(f.CP, "projects.local.json"), s+"}}")
}

func (f *Fixture) Symlink(target, link string) {
	f.T.Helper()
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(target, link); err != nil {
		f.T.Fatal(err)
	}
}

// StoreLink is a link left by the retired snapshot store; it must not block `ak kit init`.
func (f *Fixture) StoreLink() string {
	store := filepath.Join(f.CP, "runtime/agentkit-store/channels/beta/engineer/claude-code/skills/ak-cook")
	os.MkdirAll(store, 0o755)
	f.Symlink(store, filepath.Join(f.Repo, ".claude/skills/ak-cook"))
	return store
}

// Native stands in for `ak kit init` in the main checkout.
func (f *Fixture) Native() {
	f.Write(filepath.Join(f.Repo, ".claude/skills/ak-cook/SKILL.md"), "---\nname: ak-cook\n---\n")
	f.Write(filepath.Join(f.Repo, ".agents/skills/ak-cook/SKILL.md"), "---\nname: ak-cook\n---\n")
	f.Write(filepath.Join(f.Repo, ".claude/agents/planner.md"), "planner")
}

func (f *Fixture) Git(dir string, args ...string) string {
	f.T.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		f.T.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// Status is `git status --porcelain -uall`.
func (f *Fixture) Status(dir string) string { return f.Git(dir, "status", "--porcelain", "-uall") }

func (f *Fixture) IsLink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

func (f *Fixture) IsDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

func (f *Fixture) Exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func (f *Fixture) RealPath(p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}

// Fingerprint maps every path under dirs to its mode and its content hash or link target, so a
// test can show that a read-only command changed nothing.
func Fingerprint(dirs ...string) map[string]string {
	out := map[string]string{}
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			fi, err := os.Lstat(p)
			if err != nil {
				return nil
			}
			sum := fi.Mode().String()
			switch {
			case fi.Mode()&os.ModeSymlink != 0:
				dest, _ := os.Readlink(p)
				sum += " -> " + dest
			case fi.Mode().IsRegular():
				b, _ := os.ReadFile(p)
				h := sha256.Sum256(b)
				sum += " " + hex.EncodeToString(h[:])
			}
			out[p] = sum
			return nil
		})
	}
	return out
}
