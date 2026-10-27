# AGENTS.md: agent control plane

This folder is an acp control plane. It records which skills each of your projects uses; `acp`
links them into the projects and their worktrees.

## Layout

```text
projects/                     one manifest per project (no host paths)
projects.local.json           this machine's id -> repo path map (gitignored)
agentkit-releases.lock.json   the AgentKit kit version pinned per channel
global-skills.json            personal skills linked into user homes (~/.claude, ~/.codex, ...)
skills/                       your personal skills, one folder with a SKILL.md each
runtime/                      backups, locks and other machine state (gitignored)
```

## Working with acp

- `acp` shows the fleet; `acp scan` explains anything that needs attention.
- Every command takes `--json` and prints `{schema_version, kind, data}`. `acp describe --json`
  lists each command with whether it changes anything, its risk and its preview flag.
- Preview before you change anything: `--dry-run`, `--check`, or the default preview of
  `skills set`, `kit add` and `setup install-ak` (they write only with `--apply`). `remove` is a
  dry run until `--yes`.
- Change a project's skills with `acp skills set <id> --add|--remove ...` rather than editing its
  manifest by hand.

## Safety

- AgentKit owns the native content of each main checkout (`ak kit init`); acp only adds links.
- acp never replaces or removes a git-tracked path, a real folder, or a link that does not point
  into this control plane or the project's main checkout.
- Keep secrets out of this folder, its manifests and skills.
