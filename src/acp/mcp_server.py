"""acp as an MCP server: the fleet commands as tools for coding agents, over stdio.

Every tool runs `acp ... --json` in a subprocess against the control plane acp finds
($ACP_ROOT, the folder the server starts in, or the root `acp init` recorded), so the
tools behave exactly like the CLI. Mutating tools preview unless `apply` is set, and a
sync started here never opens a fix session: the calling agent is the one fixing.
"""

from __future__ import annotations

from pathlib import Path
from typing import Annotated, Any

from pydantic import Field

from mcp.server import MCPServer
from mcp.server.mcpserver.exceptions import ToolError
from mcp.types import ToolAnnotations

from acp import cli

INSTRUCTIONS = """acp manages which agent skills, agents and hooks each project gets, across
Claude Code and Codex, from one control plane of JSON manifests. Start with acp_status.
Preview before every change: acp_sync_plan before acp_sync, and acp_skills_set / acp_kit_add /
acp_upgrade without apply first. Never remove a skill or roll back a fix session unless the
user asked for it."""

server = MCPServer("acp", instructions=INSTRUCTIONS)

READ = ToolAnnotations(read_only_hint=True, open_world_hint=False)
WRITE = ToolAnnotations(read_only_hint=False, destructive_hint=False, idempotent_hint=True, open_world_hint=False)

Project = Annotated[str, Field(pattern=cli.NAME.pattern, description="Project id as listed by acp_status, e.g. 'my-app'")]
Skill = Annotated[str, Field(pattern=cli.NAME.pattern)]
Skills = Annotated[list[Skill], Field(max_length=100, description="Skill names, e.g. ['ak-cook']")]
Op = Annotated[str, Field(pattern=cli.OP_ID.pattern, description="Fix session id from acp_fix_sessions")]
Apply = Annotated[bool, Field(description="False returns the preview; set true only after the user saw it")]

MAX_CHANGES = 25  # per log; a fleet sync plan can list thousands of links


def trim(envelope: dict) -> dict:
    """Keep results readable: drop the text twin of structured logs and cap long change lists."""
    data = envelope.get("data") or {}
    if data.get("logs"):
        data.pop("text", None)
        for log in data["logs"]:
            if len(log.get("changes", [])) > MAX_CHANGES:
                log["changes_total"] = len(log["changes"])
                log["changes"] = log["changes"][:MAX_CHANGES]
    return envelope


def acp(*argv: str) -> dict[str, Any]:
    root = cli.find_root()
    if root is None:
        raise ToolError("No acp control plane found. Ask the user to run `acp init <dir>`, "
                        "or start the MCP server with ACP_ROOT set.")
    cli.use_root(root)
    _, envelope = cli.acp_json(list(argv))
    if envelope.get("error"):
        raise ToolError(envelope["error"])
    return trim(envelope)


@server.tool(title="Fleet status", annotations=READ)
def acp_status() -> dict[str, Any]:
    """Every project in the fleet: AgentKit kit, channel and installed version per target,
    personal skill links, worktree links, notes on what drifted, and a healthy flag."""
    return acp("status")


@server.tool(title="Project skills", annotations=READ)
def acp_project_skills(project: Project) -> dict[str, Any]:
    """The skills a project has (AgentKit selection and personal skills per runtime) and the
    catalogs it could add from."""
    return acp("skills", "list", project)


@server.tool(title="Sync plan", annotations=READ)
def acp_sync_plan(project: Project) -> dict[str, Any]:
    """What acp_sync would change, per main checkout and worktree, without writing anything.
    Issues listed here are things a plain sync leaves alone; 'old copy that shadows the main
    checkout' issues need acp_sync with adopt=true."""
    return acp("sync", project, "--dry-run")


@server.tool(title="Sync project", annotations=WRITE)
def acp_sync(project: Project,
             adopt: Annotated[bool, Field(description="Back up old copies in worktrees to runtime/backups and link them")] = False
             ) -> dict[str, Any]:
    """Bring a project in line with its manifest: AgentKit at the pinned version, personal
    skill links, and links in every worktree. Call acp_sync_plan first."""
    return acp("sync", project, "--no-fix", *(["--adopt"] if adopt else []))


@server.tool(title="Change project skills", annotations=WRITE)
def acp_skills_set(project: Project, add: Skills = [], remove: Skills = [], apply: Apply = False) -> dict[str, Any]:
    """Add or remove skills for a project. Returns the manifest diff and sync plan; with
    apply=true it writes the manifest and syncs. Only remove skills the user asked to remove."""
    if not add and not remove:
        raise ToolError("Pass at least one skill in add or remove.")
    argv = ["skills", "set", project]
    if add:
        argv += ["--add", ",".join(add)]
    if remove:
        argv += ["--remove", ",".join(remove)]
    return acp(*argv, *(["--apply", "--no-fix"] if apply else []))


@server.tool(title="Give a project AgentKit", annotations=WRITE)
def acp_kit_add(project: Project, apply: Apply = False) -> dict[str, Any]:
    """Add AgentKit to a project's manifest (preview), or write it and install with apply=true."""
    return acp("kit", "add", project, *(["--apply", "--no-fix"] if apply else []))


@server.tool(title="Add a repo to the fleet", annotations=WRITE)
def acp_add_project(path: Annotated[str, Field(description="Absolute path of the repo's main checkout")],
                    apply: Apply = False) -> dict[str, Any]:
    """Register a git repo in the fleet with AgentKit (engineer kit, beta channel) and sync it."""
    folder = Path(path).expanduser()
    if not folder.is_absolute() or not folder.is_dir():
        raise ToolError(f"{path} is not an existing absolute folder.")
    return acp("add", str(folder), *(["--no-fix"] if apply else ["--dry-run"]))


@server.tool(title="Fix sessions", annotations=READ)
def acp_fix_sessions() -> dict[str, Any]:
    """Coding-agent repair sessions acp started after failed syncs, newest first, and the
    fix settings (runner, target agent, attempts)."""
    return acp("fix", "list")


@server.tool(title="Fix session detail", annotations=READ)
def acp_fix_show(op: Op, transcript: bool = False) -> dict[str, Any]:
    """One fix session: diagnosis, files changed with a diff, and optionally the agent transcript."""
    return acp("fix", "show", op, *(["--transcript"] if transcript else []))


@server.tool(title="Roll back a fix session",
             annotations=ToolAnnotations(read_only_hint=False, destructive_hint=True, idempotent_hint=True,
                                         open_world_hint=False))
def acp_fix_rollback(op: Op, apply: Apply = False) -> dict[str, Any]:
    """Restore the files a fix session backed up. Without apply it lists what would be restored.
    Only roll back when the user asked to."""
    return acp("fix", "rollback", op, *(["--yes"] if apply else []))


@server.tool(title="Check the ak CLI", annotations=READ)
def acp_setup() -> dict[str, Any]:
    """The ak CLI on this machine: path, version, sign-in, `ak doctor`, and the pinned kits."""
    return acp("setup")


@server.tool(title="Upgrade AgentKit", annotations=WRITE)
def acp_upgrade(apply: Apply = False) -> dict[str, Any]:
    """Move the pin to the channel's latest kit and sync the whole fleet. Without apply it only
    reports the versions it would pin."""
    return acp("upgrade", *(["--no-fix"] if apply else ["--dry-run"]))


@server.tool(title="Command contract", annotations=READ)
def acp_describe() -> dict[str, Any]:
    """Every acp CLI command with its mutation level, risk, preview and confirm flags."""
    return acp("describe")


def serve() -> int:
    server.run()
    return 0
