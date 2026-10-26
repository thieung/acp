#!/usr/bin/env python3
"""End-to-end checks for scripts/acp against disposable repos and a fake `ak`."""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path


FAKE_AK = textwrap.dedent("""\
    #!/usr/bin/env python3
    import json, sys
    from pathlib import Path
    args = sys.argv[1:]
    if args[:1] == ["kit"] and Path(".agentkit/BROKEN").exists():
        sys.exit("error: install blocked by .agentkit/BROKEN")
    if args[:2] in (["kit", "init"], ["kit", "refresh"]):
        Path(".ak-calls").open("a").write(" ".join(args[:3] + (["--fresh"] if "--fresh" in args else [])) + "\\n")
        kit = args[2]
        target = args[args.index("--target") + 1]
        version = args[args.index("--version") + 1]
        if target == "codex" and Path(".codex/hooks.json").is_symlink():
            sys.exit("hook journal: .codex/hooks.json is not a regular file")
        skills = Path(".claude/skills" if target == "claude-code" else ".agents/skills")
        cook = skills / "ak-cook"
        if cook.is_symlink():
            sys.exit("anchor is not a real directory: " + str(cook))
        cook.mkdir(parents=True, exist_ok=True)
        (cook / "SKILL.md").write_text("---\\nname: ak-cook\\n---\\n")
        if target == "claude-code":
            Path(".claude/agents").mkdir(parents=True, exist_ok=True)
            Path(".claude/agents/planner.md").write_text("planner")
        meta = Path(".agentkit/adapters", target, kit, ".agentkit")
        meta.mkdir(parents=True, exist_ok=True)
        selection = {"mode": "list", "skills": []} if "--exclude-skills" in args else {"mode": "all"}
        (meta / "install-manifest.json").write_text(json.dumps(
            {"provenance": {"registry_version": version}, "skill_selection": selection}))
        sys.exit(0)
    if args[:1] == ["changelog"]:
        print(json.dumps({"data": {"releases": [
            {"version": "9.9.9-beta.1", "components": [{"target": "kit-engineer"}]}]}}))
        sys.exit(0)
    sys.exit(0)
    """)


# Stands in for `claude -p`: reads the evidence and diagnosis paths from the prompt and acts per FAKE_AGENT.
FAKE_AGENT = textwrap.dedent("""\
    #!/usr/bin/env python3
    import json, os, re, sys
    from pathlib import Path
    prompt = sys.argv[sys.argv.index("-p") + 1]
    evidence = json.loads(Path(re.search(r"Evidence: (\\S+)", prompt).group(1)).read_text())
    diagnosis = Path(re.search(r"write (\\S+diagnosis\\.json)", prompt).group(1))
    repo, root, mode = Path(evidence["repo"]), Path(os.environ["ACP_ROOT"]), os.environ.get("FAKE_AGENT", "fix")
    print(json.dumps({"type": "assistant", "message": {"content": [
        {"type": "text", "text": "looking at the failed " + evidence["command"]}]}}))
    result = {"cause": "the BROKEN marker blocks ak", "cause_location": "project-native",
              "fix": "removed the marker", "needs_user": None}
    if mode == "fix":
        (repo / ".agentkit/BROKEN").unlink()
    elif mode == "control-plane":
        result.update(cause_location="control-plane", needs_user="acp itself has a bug")
    elif mode == "remove-skill":
        path = root / "projects/demo.json"
        manifest = json.loads(path.read_text())
        manifest["skills"] = []
        path.write_text(json.dumps(manifest))
    elif mode == "edit-scripts":
        (root / "scripts").mkdir(exist_ok=True)
        (root / "scripts/evil.py").write_text("x")
    diagnosis.write_text(json.dumps(result))
    print(json.dumps({"type": "result", "result": "done"}))
    """)


def run(*args, cwd=None, env=None, stdin=None, check=True):
    result = subprocess.run([sys.executable, "-m", "acp", *args], cwd=cwd, env=env, input=stdin,
                            capture_output=True, text=True)
    if check and result.returncode != 0:
        raise AssertionError(f"acp {' '.join(args)} failed:\n{result.stdout}\n{result.stderr}")
    return result


def git(cwd, *args):
    subprocess.run(["git", "-C", str(cwd), *args], check=True, capture_output=True)


class AcpTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        base = Path(self.tmp.name).resolve()
        self.cp, self.repo, self.home = base / "cp", base / "repo", base / "home"
        for folder in (self.cp / "projects", self.cp / "skills/herdr", self.home / ".claude",
                       self.home / ".codex", self.repo):
            folder.mkdir(parents=True)
        (self.cp / "skills/herdr/SKILL.md").write_text("---\nname: herdr\n---\n")
        (self.cp / "agentkit-releases.lock.json").write_text(json.dumps(
            {"schemaVersion": 2, "channels": {"beta": {"engineer": "3.0.0-beta.36"}}}))
        (self.cp / "projects/demo.json").write_text(json.dumps({
            "id": "demo",
            "skills": [{"name": "herdr", "runtimes": ["claude", "codex"]}],
            "agentkit": {"kit": "engineer", "channel": "beta", "targets": ["claude-code", "codex"],
                         "skills": "all"},
        }))
        (self.cp / "projects.local.json").write_text(json.dumps(
            {"schemaVersion": 1, "repos": {"demo": str(self.repo)}}))
        fake = base / "ak"
        fake.write_text(FAKE_AK)
        fake.chmod(0o755)
        # Fix sessions are opted into per test, so no test reaches a real coding agent.
        self.env = {**os.environ, "ACP_ROOT": str(self.cp), "ACP_AK": str(fake), "HOME": str(self.home),
                    "ACP_FIX": "off"}
        for key in ("CLAUDE_CONFIG_DIR", "CODEX_HOME", "PI_CODING_AGENT_DIR", "OMP_HOME"):
            self.env.pop(key, None)

        git(self.repo, "init", "-q")
        (self.repo / ".claude/rules").mkdir(parents=True)
        (self.repo / ".claude/rules/team.md").write_text("tracked")
        git(self.repo, "add", ".")
        git(self.repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init")
        (self.repo / ".claude/settings.local.json").write_text("{}")
        # A link left by the retired snapshot store must not block `ak kit init`.
        store = self.cp / "runtime/agentkit-store/channels/beta/engineer/claude-code/skills/ak-cook"
        store.mkdir(parents=True)
        (self.repo / ".claude/skills").mkdir()
        (self.repo / ".claude/skills/ak-cook").symlink_to(store)
        self.wt = base / "wt"
        git(self.repo, "worktree", "add", "-q", "-b", "feature", str(self.wt))

    def tearDown(self):
        self.tmp.cleanup()

    def test_sync_installs_native_links_personal_and_worktree(self):
        out = run("sync", "--all", env=self.env)
        self.assertIn("demo", out.stdout)
        cook = self.repo / ".claude/skills/ak-cook"
        self.assertTrue(cook.is_dir() and not cook.is_symlink(), "ak owns skills in the main checkout")
        self.assertEqual(os.path.realpath(self.repo / ".claude/skills/herdr"), str(self.cp / "skills/herdr"))
        self.assertEqual(os.path.realpath(self.repo / ".agents/skills/herdr"), str(self.cp / "skills/herdr"))

        self.assertEqual(os.path.realpath(self.wt / ".claude/skills/ak-cook"), str(cook))
        self.assertEqual(os.path.realpath(self.wt / ".claude/agents"), str(self.repo / ".claude/agents"))
        self.assertEqual(os.path.realpath(self.wt / ".agents/skills/herdr"), str(self.cp / "skills/herdr"))
        self.assertFalse((self.wt / ".claude/rules").is_symlink(), "tracked folders stay the branch's own")
        self.assertFalse((self.wt / ".claude/settings.local.json").exists())

        self.assertEqual(run("doctor", env=self.env, check=False).returncode, 0)
        again = run("sync", "--all", env=self.env)
        self.assertIn("in sync", again.stdout)

    def test_native_upgrade_paths(self):
        # An AgentKit 2.x install: manifest without provenance, hooks.json linked into engineer/.
        legacy = self.repo / ".agentkit/adapters/codex/engineer/.agentkit"
        legacy.mkdir(parents=True)
        (legacy / "install-manifest.json").write_text(json.dumps({"kit": "engineer"}))
        (self.repo / "engineer/.codex").mkdir(parents=True)
        (self.repo / "engineer/.codex/hooks.json").write_text('{"hooks": {}}')
        (self.repo / ".codex").mkdir()
        (self.repo / ".codex/hooks.json").symlink_to(self.repo / "engineer/.codex/hooks.json")
        run("sync", "--all", env=self.env)
        hooks = self.repo / ".codex/hooks.json"
        self.assertFalse(hooks.is_symlink())
        self.assertEqual(hooks.read_text(), '{"hooks": {}}')
        calls = (self.repo / ".ak-calls").read_text().splitlines()
        self.assertEqual(calls, ["kit init engineer", "kit init engineer --fresh"])

        lock = self.cp / "agentkit-releases.lock.json"
        lock.write_text(json.dumps({"schemaVersion": 2, "channels": {"beta": {"engineer": "3.0.0-beta.40"}}}))
        run("sync", "--all", env=self.env)
        calls = (self.repo / ".ak-calls").read_text().splitlines()[2:]
        self.assertEqual(calls, ["kit refresh engineer", "kit refresh engineer"])

    def test_broken_legacy_codex_hooks_link_is_dropped(self):
        (self.repo / ".codex").mkdir()
        (self.repo / ".codex/hooks.json").symlink_to(self.repo / "engineer/.codex/hooks.json")
        run("sync", "--all", env=self.env)
        self.assertFalse((self.repo / ".codex/hooks.json").is_symlink())

    def test_hook_links_a_new_worktree_from_stdin_cwd(self):
        run("sync", "--all", env=self.env)
        late = Path(self.tmp.name).resolve() / "late"
        git(self.repo, "worktree", "add", "-q", "-b", "late", str(late))
        self.assertEqual(run("doctor", env=self.env, check=False).returncode, 1)
        run("link", "--hook", env=self.env, stdin=json.dumps({"cwd": str(late / ".claude")}))
        self.assertTrue((late / ".claude/skills/ak-cook").is_symlink())
        self.assertEqual(run("doctor", env=self.env, check=False).returncode, 0)

    def test_link_check_reports_without_writing(self):
        self.assertEqual(run("link", str(self.wt), "--check", env=self.env, check=False).returncode, 1)
        self.assertFalse((self.wt / ".claude/skills/herdr").exists())
        run("link", str(self.wt), env=self.env)
        self.assertEqual(run("link", str(self.wt), "--check", env=self.env, check=False).returncode, 0)

    def test_hook_is_silent_outside_the_fleet(self):
        out = run("link", "--hook", env=self.env, stdin=json.dumps({"cwd": self.tmp.name}))
        self.assertEqual(out.stdout, "")

    def test_worktree_drops_links_whose_source_disappeared(self):
        run("sync", "--all", env=self.env)
        (self.repo / ".claude/skills/gone").mkdir()
        run("link", str(self.wt), env=self.env)
        self.assertTrue((self.wt / ".claude/skills/gone").is_symlink())
        (self.repo / ".claude/skills/gone").rmdir()
        run("link", str(self.wt), env=self.env)
        self.assertFalse((self.wt / ".claude/skills/gone").is_symlink())

    def test_old_copy_in_a_worktree_is_reported_then_adopted(self):
        run("sync", "--all", env=self.env)
        (self.wt / ".claude/skills/ak-cook").unlink()
        (self.wt / ".claude/skills/ak-cook").mkdir()
        (self.wt / ".claude/skills/ak-cook/SKILL.md").write_text("old 2.x copy")
        out = run("sync", "--all", env=self.env, check=False)
        self.assertEqual(out.returncode, 1)
        self.assertIn("shadows the main checkout", out.stdout)
        run("sync", "--all", "--adopt", env=self.env)
        self.assertTrue((self.wt / ".claude/skills/ak-cook").is_symlink())
        backups = list((self.cp / "runtime/backups/worktree-copies").rglob("SKILL.md"))
        self.assertEqual([b.read_text() for b in backups], ["old 2.x copy"])

    def test_worktree_own_config_is_never_adopted(self):
        (self.repo / ".claude/launch.json").write_text('{"port": 3000}')
        (self.wt / ".claude/launch.json").write_text('{"port": 4100}')
        run("sync", "--all", "--adopt", env=self.env)
        self.assertEqual((self.wt / ".claude/launch.json").read_text(), '{"port": 4100}')

    def test_identical_copy_in_a_worktree_is_left_alone(self):
        run("sync", "--all", env=self.env)
        (self.wt / ".claude/skills/ak-cook").unlink()
        shutil.copytree(self.repo / ".claude/skills/ak-cook", self.wt / ".claude/skills/ak-cook")
        self.assertIn("in sync", run("sync", "--all", env=self.env).stdout)

    def test_unlink_clears_worktree_links_only(self):
        run("sync", "--all", env=self.env)
        run("unlink", str(self.wt), env=self.env)
        self.assertFalse((self.wt / ".claude/skills/ak-cook").exists())
        self.assertTrue((self.repo / ".claude/skills/ak-cook").is_dir())

    def test_remove_is_a_dry_run_until_yes(self):
        run("sync", "--all", env=self.env)
        run("remove", "demo", env=self.env)
        self.assertTrue((self.cp / "projects/demo.json").exists())
        run("remove", "demo", "--yes", env=self.env)
        self.assertFalse((self.cp / "projects/demo.json").exists())
        self.assertFalse((self.repo / ".claude/skills/herdr").exists())
        self.assertFalse((self.wt / ".claude/skills/ak-cook").exists())
        self.assertTrue((self.repo / ".claude/skills/ak-cook").is_dir(), "native content stays")
        repos = json.loads((self.cp / "projects.local.json").read_text())["repos"]
        self.assertNotIn("demo", repos)

    def test_remove_of_a_nested_folder_leaves_the_parent_worktrees_alone(self):
        run("sync", "--all", env=self.env)
        nested = self.repo / "tools/maintainer"
        (nested / ".agents/skills").mkdir(parents=True)
        (nested / ".agents/skills/herdr").symlink_to(self.cp / "skills/herdr")
        repos = json.loads((self.cp / "projects.local.json").read_text())["repos"]
        repos["nested"] = str(nested)
        (self.cp / "projects.local.json").write_text(json.dumps({"schemaVersion": 1, "repos": repos}))
        run("remove", "nested", "--yes", env=self.env)
        self.assertFalse((nested / ".agents/skills/herdr").is_symlink())
        self.assertTrue((self.wt / ".claude/skills/herdr").is_symlink(), "parent repo worktree untouched")

    def test_failed_native_install_restores_store_links(self):
        broken = Path(self.tmp.name) / "ak-fail"
        broken.write_text("#!/bin/sh\necho boom >&2\nexit 1\n")
        broken.chmod(0o755)
        out = run("sync", "--all", env={**self.env, "ACP_AK": str(broken)}, check=False)
        self.assertEqual(out.returncode, 1)
        self.assertIn("boom", out.stdout)
        self.assertTrue((self.repo / ".claude/skills/ak-cook").is_symlink())

    def test_upgrade_pins_latest_without_binary_step(self):
        run("upgrade", "--skip-binary", "--no-sync", env=self.env)
        lock = json.loads((self.cp / "agentkit-releases.lock.json").read_text())
        self.assertEqual(lock["channels"]["beta"]["engineer"], "9.9.9-beta.1")

    def test_add_registers_the_main_checkout_from_a_worktree(self):
        (self.cp / "projects/demo.json").unlink()
        (self.cp / "projects.local.json").write_text(json.dumps({"schemaVersion": 1, "repos": {}}))
        run("add", str(self.wt), "--id", "fresh", "--no-sync", env=self.env)
        repos = json.loads((self.cp / "projects.local.json").read_text())["repos"]
        self.assertEqual(repos["fresh"], str(self.repo))

    def test_global_links_and_prunes(self):
        (self.cp / "global-skills.json").write_text(json.dumps(
            {"skills": [{"name": "herdr", "runtimes": ["claude"]}]}))
        stale = self.home / ".codex/skills/herdr"
        stale.parent.mkdir(parents=True)
        stale.symlink_to(self.cp / "skills/herdr")
        self.assertEqual(run("global", "--check", env=self.env, check=False).returncode, 1)
        run("global", env=self.env)
        self.assertTrue((self.home / ".claude/skills/herdr").is_symlink())
        self.assertFalse(stale.is_symlink())
        self.assertEqual(run("global", "--check", env=self.env, check=False).returncode, 0)

    def test_hooks_install_is_idempotent_and_reversible(self):
        (self.home / ".claude/settings.json").write_text(json.dumps({"hooks": {}}))
        (self.home / ".codex/hooks.json").write_text(json.dumps({"hooks": {"SessionStart": []}}))
        run("hooks", "install", env=self.env)
        run("hooks", "install", env=self.env)
        settings = json.loads((self.home / ".claude/settings.json").read_text())
        self.assertEqual(len(settings["hooks"]["SessionStart"]), 1)
        run("hooks", "uninstall", env=self.env)
        settings = json.loads((self.home / ".claude/settings.json").read_text())
        self.assertEqual(settings["hooks"]["SessionStart"], [])

    # ------------------------------------------------------------ contract

    def json_run(self, *args, env=None, check=True):
        out = run(*args, "--json", env=env or self.env, check=check)
        return out.returncode, json.loads(out.stdout)

    def test_json_envelope_for_status_and_errors(self):
        run("sync", "--all", env=self.env)
        code, env = self.json_run("status")
        self.assertEqual((code, env["schema_version"], env["kind"]), (0, 1, "acp.status"))
        project = env["data"]["projects"][0]
        self.assertEqual(project["id"], "demo")
        self.assertTrue(project["healthy"])
        self.assertEqual({t["target"] for t in project["agentkit"]["targets"]}, {"claude-code", "codex"})
        code, env = self.json_run("sync", "nope", check=False)
        self.assertEqual(code, 1)
        self.assertIn("no project 'nope'", env["error"])

    def test_describe_lists_every_command_with_previews_for_mutations(self):
        _, env = self.json_run("describe")
        commands = {c["name"]: c for c in env["data"]["commands"]}
        self.assertTrue({"sync", "skills set", "fix rollback", "setup install-ak", "web"} <= set(commands))
        self.assertEqual([n for n, c in commands.items() if c["mutation"] and not c["preview"]], [])

    # -------------------------------------------------------------- skills

    def test_skills_set_previews_then_applies(self):
        run("sync", "--all", env=self.env)
        (self.cp / "skills/extra").mkdir()
        (self.cp / "skills/extra/SKILL.md").write_text("---\nname: extra\n---\n")
        manifest = self.cp / "projects/demo.json"
        before = manifest.read_text()
        _, env = self.json_run("skills", "set", "demo", "--add", "extra", "--remove", "ak-cook")
        self.assertFalse(env["data"]["applied"])
        self.assertEqual(manifest.read_text(), before, "a preview writes nothing")
        self.assertTrue(any("+ .claude/skills/extra" in c for log in env["data"]["plan"] for c in log["changes"]))
        code, env = self.json_run("skills", "set", "demo", "--add", "extra", "--remove", "ak-cook", "--apply")
        self.assertEqual(code, 0)
        data = json.loads(manifest.read_text())
        self.assertEqual(data["agentkit"]["exclude"], ["ak-cook"])
        self.assertIn({"name": "extra", "runtimes": ["claude", "codex"]}, data["skills"])
        self.assertTrue((self.repo / ".claude/skills/extra").is_symlink())
        self.assertIn("kit init engineer --fresh", (self.repo / ".ak-calls").read_text().splitlines()[-1])
        self.assertEqual(run("doctor", env=self.env, check=False).returncode, 0)

    def test_kit_add_previews_then_installs(self):
        bare = Path(self.tmp.name).resolve() / "bare"
        bare.mkdir()
        (self.cp / "projects/bare.json").write_text(json.dumps({"id": "bare", "skills": []}))
        repos = json.loads((self.cp / "projects.local.json").read_text())["repos"]
        repos["bare"] = str(bare)
        (self.cp / "projects.local.json").write_text(json.dumps({"schemaVersion": 1, "repos": repos}))
        run("kit", "add", "bare", "--targets", "claude-code", env=self.env)
        self.assertNotIn("agentkit", json.loads((self.cp / "projects/bare.json").read_text()))
        run("kit", "add", "bare", "--targets", "claude-code", "--apply", env=self.env)
        self.assertEqual(json.loads((self.cp / "projects/bare.json").read_text())["agentkit"]["kit"], "engineer")
        self.assertTrue((bare / ".claude/skills/ak-cook/SKILL.md").is_file())

    # ----------------------------------------------------------------- fix

    def fix_env(self, mode, **extra):
        agent = Path(self.tmp.name) / "claude"
        agent.write_text(FAKE_AGENT)
        agent.chmod(0o755)
        (self.repo / ".agentkit").mkdir(exist_ok=True)
        (self.repo / ".agentkit/BROKEN").write_text("blocks ak")
        return {**self.env, "ACP_FIX": "on", "ACP_CLAUDE": str(agent), "FAKE_AGENT": mode, **extra}

    def test_failed_sync_is_fixed_by_the_agent_and_can_be_rolled_back(self):
        env = self.fix_env("fix")
        code, out = self.json_run("sync", "--all", env=env)
        self.assertEqual(code, 0)
        fix = out["data"]["fix_sessions"][0]
        self.assertEqual((fix["state"], fix["attempt"]), ("fixed", 1))
        self.assertFalse((self.repo / ".agentkit/BROKEN").exists())
        self.assertEqual(run("doctor", env=self.env, check=False).returncode, 0)

        _, shown = self.json_run("fix", "show", fix["op"])
        self.assertTrue(any("looking at the failed sync" in e["text"] for e in shown["data"]["events"]))
        self.assertIn("BROKEN", shown["data"]["diff"])
        evidence = json.loads((self.cp / "runtime/fix-sessions" / fix["op"] / "evidence.json").read_text())
        self.assertIn("install blocked", "\n".join(evidence["failure"][0]["details"]))

        _, dry = self.json_run("fix", "rollback", fix["op"])
        self.assertFalse(dry["data"]["applied"])
        self.assertFalse((self.repo / ".agentkit/BROKEN").exists())
        code, done = self.json_run("fix", "rollback", fix["op"], "--yes")
        self.assertEqual(code, 0)
        self.assertTrue(done["data"]["verified"])
        self.assertTrue((self.repo / ".agentkit/BROKEN").exists())
        self.assertTrue((self.repo / ".claude/skills/ak-cook").is_symlink(), "the pre-fix store link is back")

    def test_fix_stops_when_the_cause_is_in_the_control_plane(self):
        code, out = self.json_run("sync", "--all", env=self.fix_env("control-plane"), check=False)
        self.assertEqual(code, 1)
        fix = out["data"]["fix_sessions"][0]
        self.assertEqual(fix["state"], "gave-up")
        self.assertIn("acp itself has a bug", fix["reason"])
        self.assertTrue(Path(fix["report"]).is_file())
        self.assertTrue((self.repo / ".agentkit/BROKEN").exists())

    def test_fix_never_keeps_a_removed_skill(self):
        code, out = self.json_run("sync", "--all", env=self.fix_env("remove-skill"), check=False)
        self.assertEqual(code, 1)
        fix = out["data"]["fix_sessions"][0]
        self.assertEqual(fix["state"], "violated")
        self.assertIn("removed personal skill herdr", fix["reason"])
        names = [s["name"] for s in json.loads((self.cp / "projects/demo.json").read_text())["skills"]]
        self.assertEqual(names, ["herdr"], "the manifest is restored")

    def test_fix_reports_a_control_plane_edit_without_reverting_it(self):
        code, out = self.json_run("sync", "--all", env=self.fix_env("edit-scripts"), check=False)
        self.assertEqual(code, 1)
        fix = out["data"]["fix_sessions"][0]
        self.assertEqual(fix["state"], "violated")
        self.assertIn("scripts/", fix["reason"])
        self.assertTrue((self.cp / "scripts/evil.py").exists())

    def test_fix_gives_up_after_its_attempts(self):
        env = self.fix_env("nothing", ACP_FIX_ATTEMPTS="2")
        code, out = self.json_run("sync", "--all", env=env, check=False)
        fix = out["data"]["fix_sessions"][0]
        self.assertEqual((code, fix["state"], fix["attempt"]), (1, "gave-up", 2))
        prompt = (self.cp / "runtime/fix-sessions" / fix["op"] / "attempt-2/prompt.txt").read_text()
        self.assertIn("Attempt 1 did not fix it", prompt)

    def test_missing_agent_ends_the_session_instead_of_hanging_it(self):
        env = {**self.fix_env("fix"), "ACP_CODEX": "", "ACP_FIX_TARGET": "codex", "PATH": "/usr/bin:/bin"}
        code, out = self.json_run("sync", "--all", env=env, check=False)
        fix = out["data"]["fix_sessions"][0]
        self.assertEqual((code, fix["state"]), (1, "gave-up"))
        self.assertIn("`codex` is not on PATH", fix["reason"])

    def test_no_fix_flag_skips_the_agent(self):
        out = run("sync", "--all", "--no-fix", env=self.fix_env("fix"), check=False)
        self.assertEqual(out.returncode, 1)
        self.assertFalse((self.cp / "runtime/fix-sessions").exists())

    # --------------------------------------------------------------- setup

    def test_install_ak_runs_the_downloaded_installer(self):
        bindir = Path(self.tmp.name) / "bin"
        installer = Path(self.tmp.name) / "install.sh"
        installer.write_text(textwrap.dedent("""\
            mkdir -p "$AK_INSTALL_DIR"
            printf '#!/bin/sh\\necho "ak 3.0.0-beta.99 $AK_CHANNEL"\\n' | sed "s/\\$AK_CHANNEL/$AK_CHANNEL/" > "$AK_INSTALL_DIR/ak"
            chmod +x "$AK_INSTALL_DIR/ak"
            """))
        env = {**self.env, "ACP_INSTALLER_URL": installer.as_uri(), "AK_INSTALL_DIR": str(bindir)}
        _, plan = self.json_run("setup", "install-ak", env=env)
        self.assertEqual(plan["data"]["env"], {"AK_CHANNEL": "beta"})
        self.assertFalse((self.cp / "runtime/installers").exists())
        code, done = self.json_run("setup", "install-ak", "--apply", env=env)
        self.assertEqual(code, 0)
        self.assertEqual(done["data"]["version"], "ak 3.0.0-beta.99 beta")
        self.assertEqual(len(done["data"]["sha256"]), 64)

    # ----------------------------------------------------------------- web

    def test_web_needs_the_token_a_local_host_and_an_allowlisted_kind(self):
        import http.client
        import time
        server = subprocess.Popen([sys.executable, "-m", "acp", "web", "--port", "0", "--no-open"], env=self.env,
                                  stdout=subprocess.PIPE, text=True)
        try:
            url = server.stdout.readline().split("acp web: ")[1].strip()
            port = int(url.split(":")[2].split("/")[0])
            token = url.split("#token=")[1]

            def call(method, path, body=None, headers=None):
                conn = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
                conn.request(method, path, body=json.dumps(body) if body else None, headers=headers or {})
                res = conn.getresponse()
                return res.status, json.loads(res.read() or b"{}")

            auth = {"Authorization": f"Bearer {token}"}
            self.assertEqual(call("GET", "/api/status")[0], 401)
            self.assertEqual(call("GET", "/api/status", headers={**auth, "Host": "evil.test"})[0], 403)
            self.assertEqual(call("GET", "/api/status", headers={**auth, "Origin": "http://evil.test"})[0], 403)
            status, env = call("GET", "/api/status", headers=auth)
            self.assertEqual((status, env["kind"]), (200, "acp.status"))
            status, env = call("GET", "/api/plan/demo", headers=auth)
            self.assertEqual((status, env["kind"]), (200, "acp.sync"))
            self.assertIn("logs", env["data"])
            self.assertEqual(call("GET", "/api/plan/..%2Fx", headers=auth)[0], 404)
            self.assertEqual(call("POST", "/api/run", {"kind": "rm", "args": {}}, auth)[0], 400)
            self.assertEqual(call("POST", "/api/run", {"kind": "sync", "args": {"id": "../x"}}, auth)[0], 400)
            status, record = call("POST", "/api/run", {"kind": "skills.preview",
                                                       "args": {"id": "demo", "add": ["herdr"]}}, auth)
            self.assertEqual(status, 202)
            for _ in range(100):
                _, record = call("GET", "/api/ops/" + record["op"], headers=auth)
                if record["state"] != "running":
                    break
                time.sleep(0.2)
            self.assertEqual(record["envelope"]["kind"], "acp.skills.set")
        finally:
            server.terminate()
            server.wait(timeout=10)


if __name__ == "__main__":
    unittest.main()
