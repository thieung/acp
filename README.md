# acp

One control plane for the skills, agents and hooks that Claude Code and Codex get in each of
your projects. acp installs AgentKit at the version you pinned, links your own skills, and
reaches into every git worktree.

acp is a single Go binary for macOS and Linux (arm64, amd64). It has three faces: the
terminal, a local web dashboard, and a read-only MCP server for agents.

## Install

```bash
gh release download <tag> -R thieung/acp -p install.sh -O - | sh
# once the repository is public:
curl -fsSL https://github.com/thieung/acp/releases/download/<tag>/install.sh | sh
```

The installer:
1. Picks the archive for your machine from `checksums.txt` and checks its sha256.
2. Puts `acp` in `~/.local/bin` (`ACP_INSTALL_DIR`).
3. Runs `acp init`.

It only replaces an earlier acp binary or a link to the old Python `scripts/acp`; anything else at that path stops it. `ACP_VERSION` picks a tag, and `ACP_NO_INIT=1` skips init.

From source: `go build -o ~/.local/bin/acp ./cmd/acp` (Go 1.26).

## Use

```bash
acp                    # fleet table: AgentKit version, personal skills, worktrees
acp sync               # sync the project you are in (main checkout or worktree)
acp upgrade            # ak binary + newest kit pin of the channel + sync the fleet
acp add [path]         # add a repo to the fleet, then sync it
acp new <path>         # create (or git init) a project, add it, AgentKit for claude by default
acp remove <id> --yes  # drop a repo and remove the links acp made
acp doctor             # like status, exit 1 on drift
acp scan               # read-only health check in plain language
acp explain <code>     # what a scan finding means and how to fix it
acp web                # dashboard on 127.0.0.1:8780; every change is previewed first
acp mcp                # MCP server over stdio: scan, status, explain, fix preview
acp skills set <id> --add herdr --remove ak-shopify   # preview; add --apply to write
acp kit add <id> --apply
acp setup              # is ak installed and signed in, doctor, pins
acp setup install-ak --apply
acp setup login --api-key-stdin <key.txt   # sign in to AgentKit without a terminal prompt
acp setup link-cli     # preview pointing ~/.local/bin/acp at this binary; add --apply
acp setup install-clt  # macOS: open Apple's installer for the Command Line Tools (git); add --apply
acp connect            # preview registering acp mcp with Claude Code and Codex; add --apply
acp describe --json    # every command's contract for agents (acp.describe/2)
```

Every command takes `--json` and prints `{schema_version, kind, data}`, plus `error` when it
fails. `acp sync --dry-run` previews.

## New projects

`acp new <path>` makes a project in one step. A missing folder is created (its parent must exist),
an existing folder that is not a repo gets `git init`, and an existing repo is added like
`acp add`. acp then installs AgentKit for the runtimes you pick and links personal skills.

```bash
acp new ~/code/foo                              # AgentKit for Claude Code only
acp new ~/code/foo --runtimes claude,codex --skills html,html-plan
acp new ~/code/foo --dry-run                    # every step, nothing written
```

Defaults come from `project-defaults.json` in the control plane; flags win over it, and
`--skills` adds to its skills:

```json
{
  "schemaVersion": 1,
  "agentkit": {"kit": "engineer", "channel": "beta"},
  "runtimes": ["claude"],
  "skills": ["html"]
}
```

Without the file acp uses `engineer`, `beta`, `claude` and no personal skills. A typo or a
wrong type in the file is an error.

- No commit is made. When acp ran `git init`, it lists the paths AgentKit and acp created in a
  marked block of `.git/info/exclude`, so a first `git add -A` does not commit them. Files that
  were already in the folder, and files you add later, stay visible. Skills added later with
  `acp skills set` are not added to the block. `acp remove <id> --yes` takes the block out.
- acp refuses `/`, your home folder, the control plane, agent homes, folders inside another
  repo, `.git` folders and bare repos, and creating or git-initing anything outside your home
  folder. It stops before writing anything when the channel has no kit pin (run `acp upgrade`)
  or `ak` is missing, unless you pass `--no-sync`.
- If AgentKit fails part way, or you passed `--no-sync`, acp keeps what it made and tells you
  to run `acp new <path>` again, which finishes the job with the settings and id the first run
  chose (flags that would change them are refused). Once a run finished, `acp new` on the same
  folder points you to `acp sync`.
- acp warns when the session-start hooks are missing (`acp hooks install`); worktrees you add
  later link themselves only with them.

The dashboard has the same flow under "Tạo dự án mới".

## Where acp looks

`acp init` creates or adopts a control plane (`projects/`, the AgentKit lock, the machine
overlay, `runtime/`, `AGENTS.md`). It ships no personal skills.

init records the control plane it set up, so later commands find it without `ACP_ROOT`. With
`ACP_ROOT` set it records nothing: the variable already wins, and it is usually a one-shell
override, such as a throwaway control plane, that must not move the root other shells, the app
and the hooks use. Run `acp init --root <dir>` without `ACP_ROOT` to record one.

Without `--root`, init uses the first of these that applies, so rerunning the installer never moves the control plane:
1. `ACP_ROOT`.
2. The control plane it recorded last time.
3. `~/agent-control-plane`.

Other commands look in this order:
1. `ACP_ROOT`.
2. The git checkout you are in, if it is a control plane with `projects.local.json`.
3. The recorded root.
4. `~/agent-control-plane`.

## Links and worktrees

acp links skills into each checkout and worktree. It never removes or replaces a path that git tracks.

Each checkout keeps a ledger of the links acp made at `<git-dir>/acp-links.json`. Each worktree also gets its own exclude file at `<git-dir>/acp-exclude`, so its links stay out of `git status`. The exclude is wired up like this:
- The repo config has `[includeIf "gitdir:<that git dir>"] path = <git-dir>/acp-config`.
- `acp-config` sets `core.excludesFile` to `acp-exclude`.

This exclude applies to that worktree only:
- A worktree added from it inherits nothing.
- A worktree recreated under the same name does not reuse the old exclude.
- The main checkout gets no exclude.

`acp unlink` removes everything acp made. Every write holds a per-repo lock and has timeouts.

## Scan, web and MCP

`acp scan` writes nothing. It checks each project, each worktree and the shared pieces (hooks, MCP servers, global skills, ak), with 20 seconds for each part. Each finding has a stable code, a severity (`error`, `warn`, `decide`, `info`) and four plain-language parts: what happened, why it matters, what happens if you ignore it, and what to do. A finding that acp can fix also carries the preview command.

`acp web` listens on loopback only. The URL carries a one-time code, which the server exchanges for a bearer token, and it refuses unknown Host and Origin headers. A change goes through these steps:
1. The server runs the preview and keeps a digest of its result.
2. You apply only while that digest still matches.
3. Right before applying, the server runs the preview again. If the result changed, it returns 409.

One change runs at a time.

`acp web --port 0 --no-open --print-url --exit-with-parent` is how an app runs the dashboard. It
picks a free port and prints the `sign in:` line without a terminal. It stops when its stdin
closes, after waiting for a running change.

`acp mcp` has four read-only tools: `acp_scan`, `acp_status`, `acp_explain`, `acp_fix_preview`. An agent cannot apply anything through MCP. A person runs the command or uses the dashboard.

```bash
acp connect --apply    # or by hand:
claude mcp add --scope user acp -- ~/.local/bin/acp mcp
codex mcp add acp -- ~/.local/bin/acp mcp
```

## Desktop app (macOS)

`acp.app` is for people who do not use a terminal. It bundles acp and shows the same dashboard
in a window. From there you can:
- Sign in to AgentKit with an API key. The key reaches `ak` on stdin only.
- Point `~/.local/bin/acp` at the bundled binary.
- Connect `acp mcp` to Claude Code and Codex.

Each change is previewed first. Sparkle keeps the app up to date once a release is signed.

Drag `acp.app` from the DMG to Applications. `install.sh` leaves a `~/.local/bin/acp` that points
into the app alone. To use the CLI on its own again, remove that link and reinstall.

Releases attach the DMG only when it is signed with a Developer ID and notarized. Building,
signing, the secrets the maintainer supplies and removing the app are covered in
[desktop/README.md](desktop/README.md).

## Develop

```bash
go vet ./... && go test -race ./...
sh scripts/build-release.sh v0.0.0-dev && sh scripts/tests/test-install.sh
cd desktop && swift test && cd ..                    # macOS app
sh scripts/build-desktop.sh v0.0.0-dev && sh scripts/tests/desktop-smoke.sh dist/desktop/acp.app v0.0.0-dev
```

CI runs the tests on Ubuntu and macOS, then tries the installer. On macOS it also builds the app
(signed ad hoc), runs the smoke test and keeps the DMG as a workflow artifact. Pushing a tag `v*` runs the release workflow:
1. It builds four archives (darwin/linux × arm64/amd64).
2. It writes `checksums.txt` and `install.sh`.
3. It creates the GitHub Release. A tag with a `-` in it is a prerelease.
4. When the signing and notary secrets exist, it builds, notarizes and smokes `acp.app`. Then it attaches the DMG and `desktop-checksums.txt`.

`site/index.html` is the bilingual guide (English and Vietnamese); open it in a browser.
