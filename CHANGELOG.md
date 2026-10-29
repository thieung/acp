# Changelog

## Unreleased

- New `acp new <path>`: create a folder (or git init an existing one, or take an existing repo),
  add it to the fleet, install AgentKit for `--runtimes` (default `claude`) and link personal
  skills from `project-defaults.json` plus `--skills`. When acp ran `git init`, the paths it and
  AgentKit created go into a marked `.git/info/exclude` block; `acp remove --yes` removes it. A
  failed run finishes by re-running `acp new`. The dashboard has a "Tạo dự án mới" form.
- New macOS app, `acp.app`: a Swift + WKWebView shell around the bundled acp. It shows the `acp
  web` dashboard, gives acp a clean, allow-listed environment and offers to move itself to
  Applications. Sparkle updates it once a feed and key are configured.
  `scripts/build-desktop.sh` builds a universal app and DMG, and `scripts/tests/desktop-smoke.sh`
  launches it. CI builds and smokes it; a release attaches the DMG only when it is signed and
  notarized.
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
