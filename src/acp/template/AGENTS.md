# AGENTS.md: agent control plane

This repository decides which agent skills, agents and hooks each project gets, for Claude
Code and Codex. The `acp` CLI applies it; AgentKit's `ak` owns the native install.

## Layout

```text
projects/<id>.json            desired state of one project (no machine paths)
projects.local.json           gitignored: project id -> absolute path on this machine
agentkit-releases.lock.json   AgentKit kit version pinned per channel
skills/<name>/SKILL.md        personal skills, linked into projects by their manifests
global-skills.json            personal skills linked into ~/.claude and ~/.codex
runtime/                      gitignored: backups, fix sessions, settings
```

## Working here

- Read state with `acp --json` (or the MCP tool `acp_status`) before changing anything.
- Add a repo with `acp add <path>`; change its skills with `acp skills set <id> --add|--remove`
  (a preview until `--apply`); give it AgentKit with `acp kit add <id>`.
- Preview every mutation first. `acp describe --json` lists which commands mutate, their
  risk, and their preview and confirm flags.
- Never edit `.claude/`, `.agents/` or `.codex/` inside a project by hand: `ak` and `acp sync`
  own them. Never symlink AgentKit skills into a main checkout.
- Do not remove a skill or a project unless the user asked for it.

## Fix sessions

When a sync fails, acp backs up the project's agent folders and hands the failure to a coding
agent. That agent may change only the project's agent folders and this repo's manifest,
overlay and lock entries. It never removes a skill or target, never switches kit or channel,
and never edits `skills/` or `hooks/`; acp checks this after every attempt. If you are that
agent, follow the prompt's limits and write the diagnosis it asks for.
