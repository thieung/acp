# Changelog

## Unreleased

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
