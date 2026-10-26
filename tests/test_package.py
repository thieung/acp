"""The packaged pieces: `acp init`, finding the control plane, the guide page and the MCP server."""

from __future__ import annotations

import http.client
import json
import os
import subprocess
import sys

import pytest

from mcp import Client
from mcp.client.stdio import StdioServerParameters


def acp(*args, cwd=None, env=None):
    return subprocess.run([sys.executable, "-m", "acp", *args], cwd=cwd, env=env, capture_output=True, text=True)


@pytest.fixture
def home(tmp_path):
    """A clean HOME and config dir, with no ACP_ROOT leaking in from the developer's shell."""
    env = {k: v for k, v in os.environ.items() if k not in ("ACP_ROOT", "CLAUDE_CONFIG_DIR", "CODEX_HOME")}
    env.update(HOME=str(tmp_path / "home"), XDG_CONFIG_HOME=str(tmp_path / "config"), ACP_FIX="off")
    (tmp_path / "home").mkdir()
    return env


@pytest.fixture
def plane(tmp_path, home):
    result = acp("init", str(tmp_path / "plane"), env=home)
    assert result.returncode == 0, result.stderr
    return tmp_path / "plane"


@pytest.fixture
def anyio_backend():
    return "asyncio"


def test_init_creates_the_template_and_records_the_default_root(tmp_path, home, plane):
    names = {p.relative_to(plane).as_posix() for p in plane.rglob("*")}
    assert {"AGENTS.md", "README.md", ".gitignore", "agentkit-releases.lock.json", "global-skills.json",
            "projects", "projects.local.json.example", "skills/README.md"} <= names
    assert not any(n.endswith(".gitkeep") or n == "gitignore" for n in names)
    config = json.loads((tmp_path / "config/acp/config.json").read_text())
    assert config["root"] == str(plane)


def test_init_previews_and_refuses_a_folder_in_use(tmp_path, home, plane):
    dry = acp("init", str(tmp_path / "other"), "--dry-run", "--json", env=home)
    data = json.loads(dry.stdout)["data"]
    assert ".gitignore" in data["files"] and not (tmp_path / "other").exists()
    again = acp("init", str(plane), env=home)
    assert again.returncode != 0 and "not empty" in again.stderr


def test_the_control_plane_is_found_from_inside_it_from_the_config_or_not_at_all(tmp_path, home, plane):
    inside = acp("status", "--json", cwd=plane / "skills", env=home)
    assert json.loads(inside.stdout)["kind"] == "acp.status"
    elsewhere = tmp_path / "somewhere"
    elsewhere.mkdir()
    via_config = acp("status", "--json", cwd=elsewhere, env=home)
    assert json.loads(via_config.stdout)["exit_code"] == 0
    (tmp_path / "config/acp/config.json").unlink()
    lost = acp("status", "--json", cwd=elsewhere, env=home)
    envelope = json.loads(lost.stdout)
    assert lost.returncode == 2 and "acp init" in envelope["error"]


def test_session_hook_runs_the_installed_package_against_this_root(plane):
    from acp import cli
    cli.use_root(plane)
    command = cli.hook_command()
    assert f"ACP_ROOT={plane}" in command and "-m acp link --hook" in command and cli.HOOK_MARK in command


def test_web_serves_the_guide(home, plane):
    server = subprocess.Popen([sys.executable, "-m", "acp", "web", "--port", "0", "--no-open"],
                              env={**home, "ACP_ROOT": str(plane)}, stdout=subprocess.PIPE, text=True)
    try:
        url = server.stdout.readline().split("acp web: ")[1].strip()
        port = int(url.split(":")[2].split("/")[0])
        conn = http.client.HTTPConnection("127.0.0.1", port, timeout=30)
        conn.request("GET", "/guide")
        res = conn.getresponse()
        res.read()
        assert (res.status, res.getheader("Location")) == (301, "/guide/")
        for path, kind in (("/guide/", "text/html"), ("/guide/fonts/barlow-400-latin.woff2", "font/woff2")):
            conn.request("GET", path)
            res = conn.getresponse()
            assert res.status == 200 and res.getheader("Content-Type").startswith(kind), path
            res.read()
        for path in ("/guide/../pyproject.toml", "/guide/fonts/x.ttf", "/guide/cli.py"):
            conn.request("GET", path)
            res = conn.getresponse()
            res.read()
            assert res.status == 404, path
    finally:
        server.terminate()
        server.wait(timeout=10)


@pytest.mark.anyio
async def test_mcp_tools_run_acp_and_report_errors_as_tool_errors(monkeypatch, home, plane):
    for key, value in home.items():
        monkeypatch.setenv(key, value)
    monkeypatch.setenv("ACP_ROOT", str(plane))
    from acp.mcp_server import server
    async with Client(server) as client:
        tools = {t.name: t for t in (await client.list_tools()).tools}
        assert {"acp_status", "acp_sync_plan", "acp_sync", "acp_skills_set", "acp_fix_rollback"} <= set(tools)
        assert tools["acp_status"].annotations.read_only_hint
        assert tools["acp_fix_rollback"].annotations.destructive_hint
        status = await client.call_tool("acp_status", {})
        assert not status.is_error and status.structured_content["kind"] == "acp.status"
        empty = await client.call_tool("acp_skills_set", {"project": "demo"})
        assert empty.is_error
        unknown = await client.call_tool("acp_sync_plan", {"project": "nope"})
        assert unknown.is_error
        bad = await client.call_tool("acp_project_skills", {"project": "../etc"})
        assert bad.is_error


@pytest.mark.anyio
@pytest.mark.parametrize("mode", ["legacy", "auto"])
async def test_mcp_over_stdio_in_both_protocol_eras(home, plane, mode):
    params = StdioServerParameters(command=sys.executable, args=["-m", "acp", "mcp"],
                                   env={**home, "ACP_ROOT": str(plane)})
    async with Client(params, mode=mode) as client:
        result = await client.call_tool("acp_describe", {})
        assert not result.is_error
        assert "sync" in {c["name"] for c in result.structured_content["data"]["commands"]}
