# Changelog

## Unreleased

- Skill presets for AgentKit skills. `acp skills set <id> --preset minimal,pr` installs short,
  combinable lists instead of a whole kit: `minimal` (brainstorm, plan, cook, test, git, fix,
  debug, scout, vault) and `continuity` (journal, handoff, watzup, sumup) for any kit; `pr`,
  `frontend`, `backend` and `pipeline` for engineer; `marketing` for marketing. `all` brings
  the primary kit back. Each preset goes to its kit, an added kit included, and acp refuses a
  preset for a kit the project does not use. `--add` and `--remove` in the same call edit the
  list. `project-defaults.json` takes `agentkit.skills` (presets or a list of skill names), so
  `acp new` can start projects with a short list; its dry run names the skills.
- `scripts/build-release.sh` and `scripts/build-desktop.sh` run on their own with a version
  manager's `GOROOT` while another go is first on `PATH`: like `scripts/verify.sh`, they switch to
  the go that owns `GOROOT` instead of failing with "compile: version X does not match go tool
  version Y". The three share `scripts/lib/use-goroot-go.sh`.

## 0.2.0-beta.1 — 2026-11-04

- `acp scan`, `acp explain` and the MCP tools `acp_scan`/`acp_explain` understand the manifest
  key `plugins` (Claude Code plugins pinned per project). New codes `plugin_not_installed`,
  `plugin_not_enabled`, `plugin_version_drift`, `plugin_user_scope_enabled` and
  `plugin_runtime_unsupported` match the Python acp's `status` and `sync --dry-run`; each names
  the `claude plugin ...` command to run. Scan only reads `installed_plugins.json` and the two
  `settings.json` files.
- A project can use several AgentKit kits. `acp kit add <id> --kit NAME` adds a kit to
  `agentkit.extra_kits` next to the primary kit, sharing its channel and targets, and
  `acp kit remove <id> --kit NAME` uninstalls it. `acp sync` installs the primary kit first on
  each target and, on Codex, refreshes it again after another kit ran so its shared agents win
  ([bestagentkits/agentkit#2980](https://github.com/bestagentkits/agentkit/issues/2980)); after
  an uninstall it refreshes the primary kit, which writes back the shared files the uninstall
  deleted ([bestagentkits/agentkit#2981](https://github.com/bestagentkits/agentkit/issues/2981)).
  `acp status`, `acp skills list|set`, `acp scan`, `acp upgrade` and the dashboard cover every
  kit; status and skills list put added kits under `agentkit.extra_kits`.
- acp now notices AgentKit files that went missing while the installed version still matches,
  for example after uninstalling a sibling kit that shared them
  ([bestagentkits/agentkit#2981](https://github.com/bestagentkits/agentkit/issues/2981)). It
  reads the files each target's install recorded in
  `.agentkit/adapters/<target>/<kit>/.agentkit/native-skill-paths.json` and judges only the ones
  inside the checkout:
  - `acp scan` reports the new code `kit_files_missing`, with `acp sync` as the fix.
  - `acp status` adds `files_missing` to each target and marks the target not ok, so
    `acp doctor` exits 1.
  - `acp sync` runs `ak kit refresh` at the same version, which writes the files back.
- `acp link` (and so `acp sync` and the worktree hook) removes links an older acp made in
  worktrees to the main checkout's per-checkout state, such as `.claude/agent-memory`, hook logs
  and runtime files. acp stopped mirroring these, but left the old links, so `git status` kept
  listing them and the `untracked_acp_links` fix that `acp scan` offered changed nothing. Only a
  link to the same path in the main checkout is removed; the data stays in the main checkout.
- `acp init` no longer records the control plane when `ACP_ROOT` is set. The variable already
  wins over the record, and a one-shell override such as a throwaway control plane moved the
  root that other shells, the app and the hooks use. `acp init --root <dir>` without `ACP_ROOT`
  records it.
- New `acp new <path>`: create a folder (or git init an existing one, or take an existing repo),
  add it to the fleet, install AgentKit for `--runtimes` (default `claude`) and link personal
  skills from `project-defaults.json` plus `--skills`. When acp ran `git init`, the paths it and
  AgentKit created go into a marked `.git/info/exclude` block; `acp remove --yes` removes it. A
  failed run finishes by re-running `acp new`. The dashboard has a "Tạo dự án mới" form.
- New macOS app, `acp.app`: a Swift + WKWebView shell around the bundled acp. It shows the `acp
  web` dashboard, gives acp a clean, allow-listed environment and offers to move itself to
  Applications. Sparkle updates it once a feed and key are configured.
  `scripts/build-desktop.sh` builds a universal app and DMG, and `scripts/tests/desktop-smoke.sh`
  launches it. A release attaches the DMG only when it is signed and notarized.
- The GitHub CI workflows (`ci.yml`, `desktop.yml`) are gone. `scripts/verify.sh` runs the same
  checks on your machine (macOS, Linux in Docker, the macOS app), and `.githooks/pre-push` runs it
  on each pushed commit once `git config core.hooksPath .githooks` is set.
- `acp setup login --api-key-stdin` signs in to AgentKit with an API key read from stdin. The
  key reaches `ak` on stdin only, never argv, the environment, logs or output. The dashboard has
  a sign-in form for it.
- `acp setup link-cli` points `~/.local/bin/acp` at the running binary. It replaces only another
  acp, backing up a curl-installed one first. `acp setup install-clt` opens Apple's installer
  for the Command Line Tools, and `acp setup` reports both.
- `acp connect` registers `acp mcp` with Claude Code and Codex through their own CLIs.
- `acp web --exit-with-parent` stops when stdin closes. acp refuses to wire hooks, links or MCP
  to a binary on a path that vanishes (temp builds, disk images, App Translocation).
  `install.sh` leaves a link to `acp.app`'s binary alone.
- acp is now a single Go binary for macOS and Linux (arm64, amd64), replacing the Python
  package. It installs from GitHub Releases through `install.sh`, which checks sha256.
- New `acp scan` and `acp explain`: a read-only health check with stable finding codes,
  severities and plain-language explanations.
- `acp web` is a dashboard where every change applies only from a fresh preview. `acp mcp`
  has read-only tools (scan, status, explain, fix preview); agents cannot apply changes.
- Links: acp never removes or replaces a git-tracked path. Each checkout keeps a ledger of
  the links acp made, and each worktree its own exclude file. `acp unlink` removes everything
  acp made.
- Dropped: the automatic fix agent, the `acp fix` commands and the MCP tools that wrote
  changes.
- The guide in `site/` describes the Go binary: install from a release, `acp scan` and
  `acp explain`, the preview-first dashboard and the read-only MCP tools.
- The guide is redesigned around a track-plan diagram of a fleet sync, set in Barlow and
  Barlow Condensed (self-hosted, with Vietnamese subsets), with an output pane under every step.

## 0.1.0 — 2026-10-26

- First release as a package, extracted from `scripts/acp` of the agent-control-plane repo.
- `acp init DIR` creates a control plane from the bundled template and records it as the
  default root; acp finds its root from `$ACP_ROOT`, the folder you are in, or that record.
- `acp mcp` serves the fleet commands as MCP tools over stdio (`mcp` SDK 2.x, answering both
  2025 and 2026 protocol clients). Changing tools preview until `apply` is set, and syncs
  started from MCP never open a fix session.
- `acp web` serves the bilingual guide at `/guide/`; the web UI links to it.
- The worktree session hook now runs `python -m acp link --hook` with `ACP_ROOT`, so it works
  from an installed package; hooks written by `scripts/acp` are still recognised.
