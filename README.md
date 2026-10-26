# acp

One control plane for the skills, agents and hooks that Claude Code and Codex get in each of
your projects. acp installs AgentKit at the version you pinned, links your own skills, reaches
into every git worktree, and hands a failed install to a coding agent that repairs it.

It ships as one CLI with three faces: the terminal, a local web UI, and an MCP server for
agents. The full guide, in English and Vietnamese, is `site/index.html` (also served by
`acp web` at `/guide/`).

## Install

```bash
uv tool install git+ssh://git@github.com/thieung/acp.git   # or: pipx install git+ssh://...
acp setup                     # checks the ak CLI; `acp setup install-ak --apply` installs it
acp init ~/agent-control-plane
acp add ~/code/my-app         # writes projects/my-app.json and syncs the repo
acp web                       # local UI on 127.0.0.1:8780
```

Python 3.10 or newer. The repository is private, so the install needs GitHub access.

## Connect an agent

```bash
claude mcp add --scope user acp -- acp mcp
codex mcp add acp -- acp mcp
```

The MCP tools mirror the CLI (`acp_status`, `acp_sync_plan`, `acp_sync`, `acp_skills_set`, …).
Changing tools return a preview until called with `apply: true`. Scripts can use the CLI
directly: every command takes `--json`, and `acp describe --json` lists which commands change
things and how to preview them.

## Where acp looks

acp works on one control plane: `$ACP_ROOT`, else the nearest folder above the current one
that holds `agentkit-releases.lock.json` and `projects/`, else the root `acp init` recorded in
`~/.config/acp/config.json`.

## Develop

```bash
uv sync
uv run pytest
uv run acp --help
```

`src/acp/cli.py` is the CLI (standard library only), `src/acp/mcp_server.py` the MCP server
(`mcp` SDK 2.x), `src/acp/web/app.html` the web UI, `src/acp/template/` what `acp init`
creates, and `site/` the guide, which the wheel ships as `acp/web/guide/`.
