"""acp: see and sync agent skills across your projects.

  acp init DIR             start a control plane (manifests, lock, skills) from the template
  acp                      fleet status (AgentKit version, personal links, worktrees)
  acp sync [ID...]         sync the project you are in, from its main checkout or any worktree
  acp sync --all           sync every project in the fleet
  acp upgrade              update the ak binary, pin the channel's latest kit, sync the fleet
  acp add [PATH]           add a repo to the fleet and sync it
  acp remove ID [--yes]    take a repo out of the fleet and drop the links acp made
  acp link [PATH]          offline: link a checkout or worktree (session hooks call this)
  acp activity             repos with Claude/Codex sessions in the last 30 days
  acp global [--check]     link personal skills from global-skills.json into user homes
  acp hooks [install|uninstall]  link worktrees automatically at session start
  acp doctor               like status, but exits 1 when anything needs a sync
  acp skills list|set ID   show or change a project's skills (set previews until --apply)
  acp kit add ID           give a project AgentKit (previews until --apply)
  acp fix run|list|show|rollback|config  coding-agent repair of failed syncs
  acp setup [install-ak|login]  check, install or sign in to the ak CLI
  acp web                  local UI on 127.0.0.1; every click runs an acp command
  acp mcp                  MCP server on stdio for coding agents
  acp describe [COMMAND]   machine-readable contract of every command

acp works on one control plane: $ACP_ROOT, else the nearest folder above the
current one that holds agentkit-releases.lock.json and projects/, else the root
`acp init` recorded in ~/.config/acp/config.json.

Every command takes --json and prints {schema_version, kind, data}. When a sync,
upgrade, add, `skills set --apply` or `kit add --apply` fails, acp backs up the
project's native files and hands the failure to a coding agent, re-runs the sync
after each attempt and stops after a few tries (`--no-fix` or ACP_FIX=off skips it).

AgentKit owns native content in each main checkout (`ak kit init`, pinned by
agentkit-releases.lock.json). acp adds personal skill links and mirrors the
untracked agent files of the main checkout into every worktree as symlinks.
"""

from __future__ import annotations

import argparse
import contextlib
import difflib
import filecmp
import functools
import hashlib
import hmac
import io
import json
import os
import re
import secrets
import shlex
import shutil
import subprocess
import sys
import threading
import time
from collections import Counter, defaultdict
from dataclasses import dataclass, field
from pathlib import Path

PACKAGE = Path(__file__).resolve().parent
WEB_PAGE = PACKAGE / "web" / "app.html"
# The guide ships inside the wheel as web/guide/; a source checkout serves site/.
GUIDE_DIRS = (PACKAGE / "web" / "guide", PACKAGE.parents[1] / "site")
GUIDE_FILES = {r"index\.html": "text/html; charset=utf-8", r"fonts/[a-z0-9-]+\.woff2": "font/woff2",
               r"img/[a-z0-9-]+\.png": "image/png"}
TEMPLATE = PACKAGE / "template"
CONFIG = Path(os.environ.get("XDG_CONFIG_HOME") or Path.home() / ".config") / "acp" / "config.json"
ROOT_MARKER = "agentkit-releases.lock.json"


def find_root() -> Path | None:
    """The control plane: $ACP_ROOT, the nearest marked folder above cwd, or the recorded default."""
    if os.environ.get("ACP_ROOT"):
        return Path(os.environ["ACP_ROOT"]).expanduser().resolve()
    here = Path.cwd().resolve()
    for folder in (here, *here.parents):
        if (folder / ROOT_MARKER).is_file() and (folder / "projects").is_dir():
            return folder
    try:
        recorded = json.loads(CONFIG.read_text(encoding="utf-8")).get("root")
    except (OSError, ValueError, AttributeError):
        recorded = None
    return Path(recorded).resolve() if recorded and Path(recorded).is_dir() else None


def use_root(root: Path) -> None:
    global ROOT, PROJECTS_DIR, OVERLAY, LOCK, SKILLS, GLOBAL_SKILLS, BACKUPS, FIX_SESSIONS, FIX_BACKUPS, SETTINGS, REPORTS
    ROOT = root
    PROJECTS_DIR = ROOT / "projects"
    OVERLAY = ROOT / "projects.local.json"
    LOCK = ROOT / ROOT_MARKER
    SKILLS = ROOT / "skills"
    GLOBAL_SKILLS = ROOT / "global-skills.json"
    BACKUPS = ROOT / "runtime" / "backups"
    FIX_SESSIONS = ROOT / "runtime" / "fix-sessions"
    FIX_BACKUPS = ROOT / "runtime" / "fix-backups"
    SETTINGS = ROOT / "runtime" / "acp-settings.json"
    REPORTS = ROOT / "plans" / "reports"


use_root(Path.cwd())  # replaced by main() before any command touches the control plane
INSTALLERS = ("https://agentkit.best/install.sh", "https://releases.agentkit.best/install.sh")
# Control-plane code a fix agent must never change.
PROTECTED = ("scripts", "hooks", "skills")

SKILL_DIRS = {"claude": ".claude/skills", "codex": ".agents/skills"}
TARGETS = {"claude-code": "claude", "codex": "codex"}
# Agent config roots mirrored from the main checkout into worktrees.
MIRROR_ROOTS = (".claude", ".agents", ".codex", ".agentkit")
# Per-checkout state that must stay local to each worktree.
MIRROR_SKIP = {
    ".DS_Store", "worktrees", "settings.local.json", "projects", "todos", "plans",
    "control-plane", "backups", "locks", "state", "session-state", "session-states", "logs",
}
MIRROR_DEPTH = 3
# Folders whose children are mirrored one by one rather than as a whole.
MIRROR_CONTAINERS = {"skills", "plugins", "adapters"}
HOOK_MARK = "acp link --hook"  # also matches hooks from the scripts/acp era

TTY = sys.stdout.isatty()
# --json: logs a command printed and the structured data it set.
EMITTED: list["Log"] = []
RESULT: dict = {}


def paint(code: str, text: str) -> str:
    return f"\033[{code}m{text}\033[0m" if TTY else text


def stamp() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%S%z")


# --------------------------------------------------------------------- data


def load_json(path: Path, default=None):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return default


def write_json(path: Path, data) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
    tmp.replace(path)


def overlay() -> dict:
    return (load_json(OVERLAY, {}) or {}).get("repos", {})


def save_overlay(repos: dict) -> None:
    write_json(OVERLAY, {"schemaVersion": 1, "repos": dict(sorted(repos.items()))})


def lock_version(channel: str, kit: str) -> str | None:
    data = load_json(LOCK, {}) or {}
    if data.get("schemaVersion") != 2:
        return None
    return (data.get("channels", {}).get(channel) or {}).get(kit)


@dataclass
class Project:
    id: str
    manifest: dict
    repo: Path | None

    @property
    def ak(self) -> dict:
        return self.manifest.get("agentkit") or {}

    def personal(self, runtime: str) -> list[str]:
        return [s["name"] for s in self.manifest.get("skills", []) if runtime in s.get("runtimes", [])]


def fleet() -> list[Project]:
    repos = overlay()
    projects = []
    for path in sorted(PROJECTS_DIR.glob("*.json")):
        manifest = load_json(path)
        repo = repos.get(manifest["id"])
        projects.append(Project(manifest["id"], manifest, Path(repo) if repo else None))
    return projects


def by_id(project_id: str) -> Project:
    for project in fleet():
        if project.id == project_id:
            return project
    raise SystemExit(f"acp: no project '{project_id}' (see `acp status`)")


# ---------------------------------------------------------------------- git


def git(cwd: Path, *args: str) -> str | None:
    result = subprocess.run(["git", "-C", str(cwd), *args], capture_output=True, text=True)
    return result.stdout.strip() if result.returncode == 0 else None


def checkout_of(path: Path) -> tuple[Path, Path]:
    """Return (main checkout, current checkout); a non-git directory is its own main."""
    path = Path(path).resolve()
    top = git(path, "rev-parse", "--show-toplevel")
    if not top:
        return path, path
    common = Path(git(path, "rev-parse", "--path-format=absolute", "--git-common-dir")).resolve()
    main = common.parent if common.name == ".git" else Path(top).resolve()
    return main, Path(top).resolve()


def worktrees(main: Path) -> list[Path]:
    """Linked worktrees of a main checkout; a folder nested inside some repo has none."""
    main = main.resolve()
    if checkout_of(main) != (main, main) or not git(main, "rev-parse", "--git-dir"):
        return []
    listing = git(main, "worktree", "list", "--porcelain") or ""
    paths = [Path(line[9:]) for line in listing.splitlines() if line.startswith("worktree ")]
    return [p.resolve() for p in paths if p.is_dir() and p.resolve() != main]


def project_for(path: Path) -> tuple[Project | None, Path, Path]:
    main, here = checkout_of(path)
    candidates = [main] if git(main, "rev-parse", "--git-dir") else [main, *main.parents]
    for project in fleet():
        if project.repo and project.repo.resolve() in candidates:
            repo = project.repo.resolve()
            return project, repo, (here if repo == main else repo)
    return None, main, here


def tracked(main: Path) -> set[str]:
    """Tracked paths under the mirror roots, including every parent directory."""
    listing = git(main, "ls-files", "-z", "--", *MIRROR_ROOTS) or ""
    paths: set[str] = set()
    for item in filter(None, listing.split("\0")):
        parts = item.split("/")
        paths.update("/".join(parts[: i + 1]) for i in range(len(parts)))
    return paths


# --------------------------------------------------------------------- links


def link_target(path: Path) -> str:
    return os.path.normpath(os.path.join(path.parent, os.readlink(path)))


def is_managed(path: Path, anchors: list[Path]) -> bool:
    """A symlink acp may replace or remove: it points into the control plane or a main checkout."""
    if not path.is_symlink():
        return False
    target = link_target(path)
    return any(target == str(a) or target.startswith(str(a) + os.sep) for a in anchors)


@dataclass
class Log:
    title: str
    ok: list[str] = field(default_factory=list)
    changes: list[str] = field(default_factory=list)
    issues: list[str] = field(default_factory=list)
    # Full tool output behind an issue, kept as evidence for a fix session.
    details: list[str] = field(default_factory=list)

    def to_dict(self) -> dict:
        return {"title": self.title.strip(), "ok": self.ok, "changes": self.changes, "issues": self.issues}

    def print(self, verbose: bool = True) -> None:
        EMITTED.append(self)
        state = paint("31", "needs attention") if self.issues else (
            paint("33", f"{len(self.changes)} change(s)") if self.changes else paint("32", "in sync"))
        print(f"{paint('1', self.title)}  {state}")
        for line in self.ok if verbose else []:
            print(f"  {paint('32', 'ok')} {line}")
        for line in self.changes:
            print(f"  {paint('33', line[:1])} {line[2:]}")
        for line in self.issues:
            print(f"  {paint('31', '!')} {line}")


def sync_personal(project: Project, checkout: Path, log: Log, dry: bool) -> None:
    for runtime, rel in SKILL_DIRS.items():
        want = set(project.personal(runtime))
        folder = checkout / rel
        for name in sorted(want):
            source = SKILLS / name
            dest = folder / name
            if not (source / "SKILL.md").is_file():
                log.issues.append(f"personal skill '{name}' is missing from {SKILLS}")
                continue
            if dest.is_symlink():
                if os.path.realpath(dest) == str(source):
                    continue
                if not is_managed(dest, [ROOT, checkout_of(checkout)[0]]):
                    log.issues.append(f"{rel}/{name} is a link acp did not make; left alone")
                    continue
                if not dry:
                    dest.unlink()
            elif dest.exists():
                log.issues.append(f"{rel}/{name} is a real directory; left alone")
                continue
            if not dry:
                folder.mkdir(parents=True, exist_ok=True)
                dest.symlink_to(source)
            log.changes.append(f"+ {rel}/{name}")
        if not folder.is_dir() or folder.is_symlink():
            continue
        for entry in sorted(folder.iterdir()):
            if not is_managed(entry, [SKILLS]):
                continue
            if entry.name in want and entry.exists():
                continue
            if not dry:
                entry.unlink()
            log.changes.append(f"- {rel}/{entry.name}")


def store_links(checkout: Path, runtime: str) -> list[Path]:
    """Links into the retired runtime/agentkit-store snapshots."""
    folder = checkout / SKILL_DIRS[runtime]
    if not folder.is_dir() or folder.is_symlink():
        return []
    return [e for e in sorted(folder.iterdir()) if is_managed(e, [ROOT / "runtime"])]


def same_tree(a: Path, b: Path) -> bool:
    if a.is_file() and b.is_file():
        return filecmp.cmp(a, b, shallow=False)
    if not (a.is_dir() and b.is_dir()):
        return False
    compare = filecmp.dircmp(a, b)
    if compare.left_only or compare.right_only or compare.funny_files:
        return False
    _, mismatch, errors = filecmp.cmpfiles(a, b, compare.common_files, shallow=False)
    return not mismatch and not errors and all(same_tree(a / d, b / d) for d in compare.common_dirs)


AK_OWNED = re.compile(
    r"^(\.claude|\.agents)/skills/ak-[^/]+$"
    r"|^\.claude/(agents|hooks|rules|scripts|output-styles)/"
    r"|^\.codex/(agents|hooks)/"
    r"|^\.agentkit/adapters/"
)


def ak_owned(key: str) -> bool:
    """Paths AgentKit generates; a differing copy of anything else is the worktree's own."""
    return bool(AK_OWNED.search(key))


def mirror(main: Path, worktree: Path, log: Log, dry: bool, adopt: bool = False) -> None:
    """Symlink every untracked agent file of the main checkout that the worktree lacks."""
    keep = tracked(main) | tracked(worktree)
    anchors = [main, ROOT]

    def walk(rel: Path, depth: int) -> None:
        for child in sorted((main / rel).iterdir()):
            if child.name in MIRROR_SKIP:
                continue
            key = (rel / child.name).as_posix()
            dest = worktree / key
            if dest.is_symlink():
                if dest.exists() or not is_managed(dest, anchors):
                    continue
                if not dry:
                    dest.unlink()
            elif dest.exists():
                container = depth == 1 or child.name in MIRROR_CONTAINERS
                if container and child.is_dir() and not child.is_symlink() and dest.is_dir():
                    if depth < MIRROR_DEPTH:
                        walk(rel / child.name, depth + 1)
                    continue
                if key in keep or child.is_symlink() or not ak_owned(key) or same_tree(child, dest):
                    continue
                # An untracked copy left by an earlier `ak` run inside the worktree shadows main.
                if not adopt:
                    log.issues.append(f"{key} is an old copy that shadows the main checkout; "
                                      "`acp sync --adopt` backs it up and links it")
                    continue
                if not dry:
                    backup = BACKUPS / "worktree-copies" / time.strftime("%Y%m%d-%H%M%S") / worktree.name / key
                    backup.parent.mkdir(parents=True, exist_ok=True)
                    shutil.move(str(dest), str(backup))
                    dest.symlink_to(child)
                log.changes.append(f"~ {key} (old copy moved to runtime/backups)")
                continue
            if key in keep or (child.is_symlink() and not child.exists()):
                continue
            if is_managed(child, [ROOT / "runtime"]):
                continue
            target = Path(os.path.realpath(child)) if child.is_symlink() else child
            if not dry:
                dest.parent.mkdir(parents=True, exist_ok=True)
                dest.symlink_to(target)
            log.changes.append(f"+ {key}")

    for root in MIRROR_ROOTS:
        if (main / root).is_dir() and not (main / root).is_symlink():
            walk(Path(root), 1)


def managed_links(checkout: Path, anchors: list[Path]):
    """Yield managed symlinks under the mirror roots without following linked directories."""
    def walk(folder: Path, depth: int):
        try:
            entries = sorted(folder.iterdir())
        except OSError:
            return
        for entry in entries:
            if entry.is_symlink():
                if is_managed(entry, anchors):
                    yield entry
            elif entry.is_dir() and depth < MIRROR_DEPTH:
                yield from walk(entry, depth + 1)

    for root in MIRROR_ROOTS:
        folder = checkout / root
        if folder.is_dir() and not folder.is_symlink():
            yield from walk(folder, 1)


def sweep(main: Path, worktree: Path, log: Log, dry: bool) -> None:
    for link in managed_links(worktree, [main, ROOT]):
        if not link.exists():
            if not dry:
                link.unlink()
            log.changes.append(f"- {link.relative_to(worktree).as_posix()} (broken)")


def unlink_all(checkout: Path, main: Path, dry: bool) -> list[str]:
    anchors = [ROOT] if checkout == main else [main, ROOT]
    removed = []
    for link in list(managed_links(checkout, anchors)):
        if not dry:
            link.unlink()
        removed.append(link.relative_to(checkout).as_posix())
    return removed


# -------------------------------------------------------------------- native


def ak_bin() -> str:
    found = os.environ.get("ACP_AK") or shutil.which("ak")
    if not found:
        raise SystemExit("acp: the AgentKit CLI `ak` is not on PATH")
    return found


def install_manifest(checkout: Path, target: str, kit: str) -> dict:
    path = checkout / ".agentkit/adapters" / target / kit / ".agentkit/install-manifest.json"
    return load_json(path, {}) or {}


def installed_version(checkout: Path, target: str, kit: str) -> str | None:
    return (install_manifest(checkout, target, kit).get("provenance") or {}).get("registry_version")


@functools.lru_cache(maxsize=None)
def kit_catalog(kit: str) -> tuple[str, ...]:
    """Every skill of a kit, as recorded by the installs across the fleet."""
    names: set[str] = set()
    for project in fleet():
        if not project.repo or (project.ak.get("kit", "engineer")) != kit:
            continue
        for target in project.ak.get("targets", list(TARGETS)):
            selection = install_manifest(project.repo, target, kit).get("skill_selection") or {}
            names.update(selection.get("skills") or [])
    return tuple(sorted(names))


def personal_catalog() -> list[str]:
    return sorted(p.parent.name for p in SKILLS.glob("*/SKILL.md"))


def selection_matches(ak: dict, manifest: dict) -> bool:
    recorded = manifest.get("skill_selection") or {}
    wanted = ak.get("skills", "all")
    excluded = set(ak.get("exclude") or [])
    if wanted == "all" and not excluded:
        return recorded.get("mode") == "all"
    if isinstance(wanted, list):
        return sorted(recorded.get("skills") or []) == sorted(wanted)
    # ak records an exclusion as an explicit list: it must equal the kit minus the exclusions.
    have = set(recorded.get("skills") or [])
    catalog = set(kit_catalog(ak.get("kit", "engineer")))
    if recorded.get("mode") == "all" or have & excluded:
        return False
    return not catalog or have == catalog - excluded


def sync_native(project: Project, main: Path, log: Log, dry: bool) -> None:
    ak = project.ak
    if not ak:
        return
    kit, channel = ak.get("kit", "engineer"), ak.get("channel", "beta")
    pin = lock_version(channel, kit)
    if not pin:
        log.issues.append(f"no {channel}/{kit} pin in {LOCK.name}; run `acp upgrade`")
        return
    for target in ak.get("targets", list(TARGETS)):
        recorded = install_manifest(main, target, kit)
        have = (recorded.get("provenance") or {}).get("registry_version")
        stale = store_links(main, TARGETS[target])
        same_selection = selection_matches(ak, recorded)
        if have == pin and same_selection and not stale:
            log.ok.append(f"AgentKit {kit} {pin} ({target})")
            continue
        why = "" if same_selection or not recorded else ", new skill selection"
        label = have or ("2.x" if recorded else "not installed")
        log.changes.append(f"~ AgentKit {kit} {target}: {label} -> {pin}{why}")
        if dry:
            continue
        # ak refuses to write through symlinked skill folders, so retired store links go first.
        saved = {link: os.readlink(link) for link in stale}
        for link in saved:
            link.unlink()
        # AgentKit 2.x linked .codex/hooks.json into its old lifecycle root; ak 3 needs a real file.
        hooks = main / ".codex/hooks.json"
        if target == "codex" and hooks.is_symlink():
            content = hooks.read_bytes() if hooks.exists() else None
            hooks.unlink()
            if content is not None:
                hooks.write_bytes(content)
        if have and same_selection:
            # A clean v3 install upgrades in place and keeps its recorded selection.
            command = [ak_bin(), "kit", "refresh", kit]
        else:
            # Nothing yet, a 2.x install, or a changed selection: start from kit-only output.
            command = [ak_bin(), "kit", "init", kit] + (["--fresh"] if recorded else [])
            if isinstance(ak.get("skills"), list):
                command += ["--skills", ",".join(ak["skills"])]
            if ak.get("exclude"):
                command += ["--exclude-skills", ",".join(ak["exclude"])]
        command += ["--channel", channel, "--version", pin, "--target", target, "--yes", "--no-interactive"]
        result = subprocess.run(command, cwd=main, capture_output=True, text=True)
        if result.returncode != 0:
            for link, target_path in saved.items():
                if not link.exists() and not link.is_symlink():
                    link.symlink_to(target_path)
            detail = [line for line in (result.stderr + result.stdout).splitlines() if line.strip()]
            errors = [line for line in detail if "rror" in line] or detail[-1:]
            log.issues.append(f"`ak kit {command[2]}` failed for {target} (exit {result.returncode}): "
                              f"{errors[0].strip() if errors else 'no output'}")
            log.details += [f"$ {shlex.join(command[1:])}", *detail[-60:]]
        elif installed_version(main, target, kit) != pin:
            log.issues.append(f"{target}: ak finished but the install manifest does not show {pin}")
        kit_catalog.cache_clear()


# ---------------------------------------------------------------------- sync


def sync_checkout(project: Project, main: Path, checkout: Path, log: Log, dry: bool,
                  adopt: bool = False) -> None:
    sync_personal(project, checkout, log, dry)
    if checkout != main:
        mirror(main, checkout, log, dry, adopt)
        sweep(main, checkout, log, dry)


def sync_project(project: Project, dry: bool, native: bool = True, adopt: bool = False) -> list[Log]:
    if not project.repo or not project.repo.is_dir():
        log = Log(project.id)
        log.issues.append(f"repo path is missing: {project.repo or 'not in projects.local.json'}")
        return [log]
    main = project.repo.resolve()
    head = Log(f"{project.id}  {main}")
    if native:
        sync_native(project, main, head, dry)
    sync_checkout(project, main, main, head, dry)
    logs = [head]
    for worktree in worktrees(main):
        log = Log(f"  worktree {worktree}")
        sync_checkout(project, main, worktree, log, dry, adopt)
        logs.append(log)
    return logs


def selected(args) -> list[Project]:
    if getattr(args, "all", False):
        return fleet()
    if getattr(args, "ids", None):
        return [by_id(i) for i in args.ids]
    project, _, _ = project_for(Path.cwd())
    if not project:
        raise SystemExit("acp: this directory is not in the fleet; use `acp add`, an ID, or --all")
    return [project]


def print_logs(logs: list[Log]) -> None:
    for log in logs:
        if log.changes or log.issues or log.title[0] != " ":
            log.print(verbose=False)


def cmd_sync(args) -> int:
    failed = False
    for project in selected(args):
        logs = sync_project(project, args.dry_run, native=not args.offline, adopt=args.adopt)
        print_logs(logs)
        failed |= not maybe_fix(project, "sync", logs, args)
    if args.dry_run:
        print(paint("2", "dry run: nothing was written"))
    return 1 if failed else 0


def cmd_link(args) -> int:
    path = Path(args.path or Path.cwd())
    if args.hook:
        try:
            payload = json.loads(sys.stdin.read() or "{}") if not sys.stdin.isatty() else {}
            path = Path(payload.get("cwd") or path)
        except (ValueError, OSError):
            pass
    if not path.is_dir():
        if args.hook:
            return 0
        raise SystemExit(f"acp: {path} does not exist")
    try:
        project, main, here = project_for(path)
        if not project:
            if not args.hook:
                print("acp: not a fleet project; nothing to link")
            return 0
        log = Log(f"{project.id}  {here}")
        sync_checkout(project, main, here, log, dry=args.check, adopt=args.adopt)
        if not args.hook:
            log.print()
        if args.check and (log.changes or log.issues):
            return 1
    except Exception as error:  # a hook must never block a session
        if not args.hook:
            raise
        print(f"acp link: {error}", file=sys.stderr)
    return 0


def cmd_unlink(args) -> int:
    main, here = checkout_of(Path(args.path or Path.cwd()))
    for rel in unlink_all(here, main, args.dry_run):
        print(f"- {rel}")
    return 0


# -------------------------------------------------------------------- status


def native_counts(main: Path, target: str, recorded: dict) -> dict:
    """AgentKit skills, agents and hooks in a main checkout for one target, plus leftover ak-* skills."""
    def entries(folder: Path, prefix: str = "") -> list[str]:
        if not folder.is_dir():
            return []
        # Personal skills named ak-* are acp's links into skills/, not AgentKit's.
        return [e.name for e in folder.iterdir() if e.name.startswith(prefix) and not e.name.startswith(".")
                and not is_managed(e, [SKILLS])]

    native = main / (".claude" if target == "claude-code" else ".codex")
    selection = set((recorded.get("skill_selection") or {}).get("skills") or [])
    skills = entries(main / SKILL_DIRS[TARGETS[target]], "ak-")
    return {"skills": len([s for s in skills if not selection or s in selection]),
            "stale": sorted(s for s in skills if selection and s not in selection),
            "agents": len(entries(native / "agents")), "hooks": len(entries(native / "hooks"))}


def project_status(project: Project) -> dict:
    """Everything the fleet table, --json and the web UI show for one project."""
    info = {"id": project.id, "repo": str(project.repo) if project.repo else None, "agentkit": None,
            "personal": {"linked": 0, "total": 0, "skills": {r: project.personal(r) for r in SKILL_DIRS}},
            "worktrees": {"linked": 0, "total": 0}, "notes": []}
    notes = info["notes"]
    if not project.repo or not project.repo.is_dir():
        notes.append(f"repo path is missing: {project.repo}")
        info["healthy"] = False
        return info
    main = project.repo.resolve()
    ak = project.ak
    if ak:
        kit, channel = ak.get("kit", "engineer"), ak.get("channel", "beta")
        pin = lock_version(channel, kit)
        targets = []
        for target in ak.get("targets", list(TARGETS)):
            recorded = install_manifest(main, target, kit)
            have = (recorded.get("provenance") or {}).get("registry_version") or ("2.x" if recorded else None)
            same = selection_matches(ak, recorded)
            ok = have == pin and same and not store_links(main, TARGETS[target])
            targets.append({"target": target, "installed": have, "ok": ok, "selection_ok": same,
                            **native_counts(main, target, recorded)})
            if not ok:
                notes.append(f"{target}: {have or 'not installed'}, pinned {pin}"
                             + ("" if same or not recorded else ", selection differs"))
        info["agentkit"] = {"kit": kit, "channel": channel, "pin": pin, "skills": ak.get("skills", "all"),
                            "exclude": ak.get("exclude", []), "targets": targets}
    log = Log("")
    sync_personal(project, main, log, dry=True)
    total = sum(len(project.personal(r)) for r in SKILL_DIRS)
    info["personal"].update(linked=total - len([c for c in log.changes if c[0] == "+"]), total=total)
    notes += [f"! {i}" for i in log.issues] + log.changes
    trees = worktrees(main)
    linked = 0
    for worktree in trees:
        wlog = Log("")
        sync_checkout(project, main, worktree, wlog, dry=True)
        linked += not (wlog.changes or wlog.issues)
    info["worktrees"] = {"linked": linked, "total": len(trees)}
    if linked < len(trees):
        notes.append(f"{len(trees) - linked} worktree(s) need links")
    info["healthy"] = not notes
    return info


def status_row(info: dict) -> list[str]:
    if info["repo"] is None or not Path(info["repo"]).is_dir():
        return [info["id"], "-", "-", "-"]
    versions = [paint("32" if t["ok"] else "33",
                      f"{'cc' if t['target'] == 'claude-code' else t['target']} {t['installed'] or 'none'}")
                for t in (info["agentkit"] or {}).get("targets", [])]
    trees = info["worktrees"]
    return [info["id"], " ".join(versions) or "-", f"{info['personal']['linked']}/{info['personal']['total']}",
            f"{trees['linked']}/{trees['total']}" if trees["total"] else "-"]


def visible_len(text: str) -> int:
    return len(re.sub(r"\033\[[0-9;]*m", "", text))


def cmd_status(args) -> int:
    projects = fleet()
    infos = [project_status(p) for p in projects]
    rows = [status_row(i) for i in infos]
    all_notes = {i["id"]: i["notes"] for i in infos if i["notes"]}
    orphans = sorted(set(overlay()) - {p.id for p in projects})
    for orphan in orphans:
        all_notes[orphan] = ["overlay entry without a manifest; `acp remove " + orphan + " --yes`"]
    RESULT.update(projects=infos, orphans=orphans, pins=(load_json(LOCK, {}) or {}).get("channels", {}),
                  healthy=not all_notes)
    header = ["PROJECT", "AGENTKIT", "PERSONAL", "WORKTREES"]
    widths = [max(visible_len(r[i]) for r in [header, *rows]) for i in range(len(header))]
    print("  ".join(paint("1", h.ljust(w)) for h, w in zip(header, widths)))
    for row in rows:
        mark = paint("31", "!") if row[0] in all_notes else paint("32", "✓")
        print("  ".join(c + " " * (w - visible_len(c)) for c, w in zip(row, widths)) + "  " + mark)
    pins = (load_json(LOCK, {}) or {}).get("channels", {})
    print(paint("2", "pinned: " + ", ".join(f"{c}/{k} {v}" for c, kits in pins.items() for k, v in kits.items())))
    if all_notes:
        print()
        for pid, notes in all_notes.items():
            print(f"{paint('1', pid)}")
            for note in notes[: None if args.verbose else 5]:
                sign, text = (note[0], note[2:]) if note[:2] in ("+ ", "- ", "! ", "~ ") else ("~", note)
                print(f"  {paint('31' if sign == '!' else '33', sign)} {text}")
            if len(notes) > 5 and not args.verbose:
                print(f"  {paint('2', f'... {len(notes) - 5} more (acp status -v)')}")
        print(paint("2", "\nfix: acp sync --all"))
    return 1 if (args.command == "doctor" and all_notes) else 0


# ------------------------------------------------------------------- upgrade


def resolve_latest(channel: str, kit: str) -> str:
    result = subprocess.run([ak_bin(), "changelog", "--channel", channel, "--kit", kit, "--json",
                             "--no-interactive"], capture_output=True, text=True)
    if result.returncode != 0:
        raise SystemExit(f"acp: `ak changelog` failed: {result.stderr.strip()}")
    for release in json.loads(result.stdout).get("data", {}).get("releases", []):
        if any(c.get("target") == f"kit-{kit}" for c in release.get("components", [])):
            return release["version"]
    raise SystemExit(f"acp: no {kit} release in the {channel} changelog")


def cmd_upgrade(args) -> int:
    if not args.skip_binary and not args.dry_run:
        print(f"updating the ak binary on {args.channel} ...", flush=True)
        result = subprocess.run([ak_bin(), "self-update", "--channel", args.channel, "--yes"],
                                capture_output=True, text=True)
        print((result.stdout + result.stderr).rstrip())
    kits = sorted({p.ak.get("kit", "engineer") for p in fleet()
                   if p.ak and p.ak.get("channel", "beta") == args.channel}) or ["engineer"]
    data = load_json(LOCK, {}) or {}
    data = {"schemaVersion": 2, "channels": data.get("channels", {}) if data.get("schemaVersion") == 2 else {}}
    data["channels"][args.channel] = {}  # kits the fleet no longer uses drop out of the pin
    for kit in kits:
        version = args.version or resolve_latest(args.channel, kit)
        old = lock_version(args.channel, kit)
        data["channels"].setdefault(args.channel, {})[kit] = version
        print(f"pin {args.channel}/{kit}: {old or 'none'} -> {version}")
    RESULT["pins"] = data["channels"]
    if args.dry_run:
        print(paint("2", "dry run: the binary, the lock and the fleet were left alone"))
        return 0
    write_json(LOCK, data)
    if args.no_sync:
        return 0
    failed = False
    for project in fleet():
        if project.ak.get("channel", "beta") != args.channel:
            continue
        logs = sync_project(project, dry=False)
        print_logs(logs)
        failed |= not maybe_fix(project, "upgrade", logs, args)
    return 1 if failed else 0


# --------------------------------------------------------------- add/remove


def cmd_add(args) -> int:
    main, _ = checkout_of(Path(args.path or Path.cwd()))
    project_id = args.id or re.sub(r"[^a-z0-9]+", "-", main.name.lower()).strip("-")
    path = PROJECTS_DIR / f"{project_id}.json"
    if path.exists():
        raise SystemExit(f"acp: {project_id} is already in the fleet")
    manifest = {"id": project_id, "skills": []}
    if not args.no_agentkit:
        manifest["agentkit"] = {"kit": args.kit, "channel": args.channel,
                                "targets": args.targets.split(","), "skills": "all"}
    RESULT.update(project=project_id, repo=str(main), manifest=manifest)
    if args.dry_run:
        print(f"would add {project_id} -> {main}\n{json.dumps(manifest, indent=2)}")
        print(paint("2", "dry run: nothing was written"))
        return 0
    write_json(path, manifest)
    repos = overlay()
    repos[project_id] = str(main)
    save_overlay(repos)
    print(f"added {project_id} -> {main}  (edit {path.relative_to(ROOT)} for personal skills)")
    if args.no_sync:
        return 0
    project = by_id(project_id)
    logs = sync_project(project, dry=False)
    for log in logs:
        log.print(verbose=False)
    return 0 if maybe_fix(project, "add", logs, args) else 1


def cmd_remove(args) -> int:
    repos = overlay()
    manifest = PROJECTS_DIR / f"{args.id}.json"
    if args.id not in repos and not manifest.exists():
        raise SystemExit(f"acp: no project '{args.id}'")
    repo = Path(repos[args.id]).resolve() if args.id in repos else None
    shared = repo and any(Path(v).resolve() == repo for k, v in repos.items() if k != args.id)
    dry = not args.yes
    if repo and repo.is_dir() and not shared:
        for checkout in [repo, *worktrees(repo)]:
            for rel in unlink_all(checkout, repo, dry):
                print(f"- {checkout}/{rel}")
    elif shared:
        print(f"{repo} is also used by another project id; its links stay")
    if manifest.exists():
        print(f"- projects/{args.id}.json")
    if args.id in repos:
        print(f"- projects.local.json: {args.id}")
    if dry:
        print(paint("2", "dry run: pass --yes to remove"))
        return 0
    manifest.unlink(missing_ok=True)
    repos.pop(args.id, None)
    save_overlay(repos)
    print(f"removed {args.id}; native AgentKit files in the repo were left in place")
    return 0


# ------------------------------------------------------------------ activity


def session_cwd(path: Path, codex: bool) -> str | None:
    try:
        with path.open(errors="ignore") as handle:
            for index, line in enumerate(handle):
                if index > 40:
                    return None
                try:
                    data = json.loads(line)
                except ValueError:
                    continue
                if codex and data.get("type") == "session_meta":
                    return data.get("payload", {}).get("cwd")
                if not codex and data.get("cwd"):
                    return data["cwd"]
    except OSError:
        return None
    return None


def cmd_activity(args) -> int:
    cutoff = time.time() - args.days * 86400
    home = Path.home()
    counts: dict[str, dict] = defaultdict(lambda: {"claude": 0, "codex": 0, "last": 0.0})
    sources = [(home / ".claude/projects", "*/*.jsonl", False),
               (Path(os.environ.get("CODEX_HOME", home / ".codex")) / "sessions", "*/*/*/*.jsonl", True)]
    for base, pattern, codex in sources:
        for path in base.glob(pattern):
            mtime = path.stat().st_mtime
            if mtime < cutoff:
                continue
            cwd = session_cwd(path, codex)
            if not cwd or not Path(cwd).is_dir() or cwd.startswith(("/private/", "/tmp/", "/var/")):
                continue
            main, _ = checkout_of(Path(cwd))
            if main == home.resolve() or "/.cache/" in str(main):
                continue
            entry = counts[str(main)]
            entry["codex" if codex else "claude"] += 1
            entry["last"] = max(entry["last"], mtime)
    members = {str(p.repo.resolve()): p.id for p in fleet() if p.repo and p.repo.is_dir()}
    print(paint("1", f"{'CLAUDE':>6} {'CODEX':>5}  LAST   FLEET ID              REPO"))
    for repo, entry in sorted(counts.items(), key=lambda kv: -(kv[1]["claude"] + kv[1]["codex"])):
        if entry["claude"] + entry["codex"] < args.min:
            continue
        member = members.get(repo, "")
        print(f"{entry['claude']:>6} {entry['codex']:>5}  {time.strftime('%m-%d', time.localtime(entry['last']))}  "
              f"{paint('32', member.ljust(20)) if member else paint('2', '-'.ljust(20))}  {repo}")
    idle = sorted(pid for repo, pid in members.items() if repo not in counts)
    if idle:
        print(paint("33", f"\nfleet projects with no sessions in {args.days} days: {', '.join(idle)}"))
    return 0


# -------------------------------------------------------------------- global


def global_homes() -> dict[str, Path]:
    env, home = os.environ, Path.home()
    return {
        "claude": Path(env.get("CLAUDE_CONFIG_DIR") or home / ".claude"),
        "codex": Path(env.get("CODEX_HOME") or home / ".codex"),
        "pi": Path(env.get("PI_CODING_AGENT_DIR") or home / ".pi/agent"),
        "omp": Path(env.get("AGENTKIT_OMP_HOME") or env.get("OMP_HOME")
                    or env.get("PI_CODING_AGENT_DIR") or home / ".omp/agent"),
        "pi-ak": Path(env.get("PI_AK_AGENT_DIR") or home / ".pi/profiles/pi-ak"),
        "pi-omp": Path(env.get("PI_OMP_AGENT_DIR") or home / ".omp/profiles/pi-omp/agent"),
    }


def cmd_global(args) -> int:
    declared: dict[str, set[str]] = defaultdict(set)
    for skill in (load_json(GLOBAL_SKILLS, {}) or {}).get("skills", []):
        for runtime in skill.get("runtimes", []):
            declared[runtime].add(skill["name"])
    log = Log("global skills")
    for runtime, home in global_homes().items():
        folder = home / "skills"
        for name in sorted(declared.get(runtime, ())):
            source, dest = SKILLS / name, folder / name
            if not (source / "SKILL.md").is_file():
                log.issues.append(f"{runtime}: '{name}' is missing from {SKILLS}")
            elif dest.is_symlink() and os.path.realpath(dest) == str(source):
                log.ok.append(f"{runtime}: {name}")
            elif dest.exists() or (dest.is_symlink() and not is_managed(dest, [SKILLS])):
                log.issues.append(f"{dest} exists and is not acp's link; left alone")
            else:
                if not args.check:
                    if dest.is_symlink():
                        dest.unlink()
                    folder.mkdir(parents=True, exist_ok=True)
                    dest.symlink_to(source)
                log.changes.append(f"+ {runtime}: {name}")
        if folder.is_dir():
            for entry in sorted(folder.iterdir()):
                if is_managed(entry, [SKILLS]) and (entry.name not in declared.get(runtime, ()) or not entry.exists()):
                    if not args.check:
                        entry.unlink()
                    log.changes.append(f"- {runtime}: {entry.name}")
    log.print(verbose=args.verbose)
    return 1 if log.issues or (args.check and log.changes) else 0


# --------------------------------------------------------------------- hooks


def hook_files() -> dict[str, Path]:
    homes = global_homes()
    return {"claude": homes["claude"] / "settings.json", "codex": homes["codex"] / "hooks.json"}


def hook_command() -> str:
    return f"ACP_ROOT={shlex.quote(str(ROOT))} {shlex.quote(sys.executable)} -m acp link --hook"


def has_hook(data: dict) -> bool:
    return any(HOOK_MARK in h.get("command", "")
               for group in data.get("hooks", {}).get("SessionStart", []) for h in group.get("hooks", []))


def cmd_hooks(args) -> int:
    for runtime, path in hook_files().items():
        data = load_json(path, None)
        if data is None:
            print(f"{runtime}: {path} not found; skipped")
            continue
        present = has_hook(data)
        if args.action == "status":
            print(f"{runtime}: {'installed' if present else 'not installed'} ({path})")
            continue
        if (args.action == "install") == present:
            print(f"{runtime}: already {'installed' if present else 'absent'}")
            continue
        if args.dry_run:
            print(f"{runtime}: would {args.action} the SessionStart hook in {path}")
            continue
        BACKUPS.mkdir(parents=True, exist_ok=True)
        backup = BACKUPS / f"{runtime}-{path.name}.{time.strftime('%Y%m%d-%H%M%S')}"
        shutil.copy2(path, backup)
        groups = data.setdefault("hooks", {}).setdefault("SessionStart", [])
        if args.action == "install":
            groups.append({"matcher": "startup|resume",
                           "hooks": [{"type": "command", "command": hook_command()}]})
        else:
            for group in groups:
                group["hooks"] = [h for h in group.get("hooks", []) if HOOK_MARK not in h.get("command", "")]
            data["hooks"]["SessionStart"] = [g for g in groups if g.get("hooks")]
        write_json(path, data)
        print(f"{runtime}: {args.action}ed (backup {backup})")
    return 0


# -------------------------------------------------------------- skills / kit


def default_runtimes(project: Project) -> list[str]:
    targets = project.ak.get("targets") if project.ak else None
    return [TARGETS[t] for t in targets if t in TARGETS] if targets else list(SKILL_DIRS)


def skill_kind(project: Project, name: str) -> str:
    if project.ak and name in kit_catalog(project.ak.get("kit", "engineer")):
        return "agentkit"
    if name in personal_catalog():
        return "personal"
    if name.startswith("ak-") and project.ak:
        return "agentkit"
    raise SystemExit(f"acp: unknown skill '{name}' (not in the kit catalog or {SKILLS})")


def edit_skills(project: Project, add: list[str], remove: list[str], runtimes: list[str]) -> dict:
    """Return the manifest with skills added and removed; nothing is written."""
    manifest = json.loads(json.dumps(project.manifest))
    ak = manifest.get("agentkit")
    personal = manifest.setdefault("skills", [])
    for name, adding in [(n, True) for n in add] + [(n, False) for n in remove]:
        if skill_kind(project, name) == "agentkit":
            exclude = [s for s in ak.get("exclude", []) if s != name]
            if ak.get("skills", "all") == "all":
                exclude += [] if adding else [name]
            elif adding and name not in ak["skills"]:
                ak["skills"] = sorted([*ak["skills"], name])
            elif not adding:
                ak["skills"] = [s for s in ak["skills"] if s != name]
            if exclude:
                ak["exclude"] = sorted(set(exclude))
            else:
                ak.pop("exclude", None)
            continue
        entry = next((s for s in personal if s["name"] == name), None)
        if adding:
            if entry:
                entry["runtimes"] += [r for r in runtimes if r not in entry["runtimes"]]
            else:
                personal.append({"name": name, "runtimes": list(runtimes)})
        elif entry:
            entry["runtimes"] = [r for r in entry["runtimes"] if r not in runtimes]
            if not entry["runtimes"]:
                personal.remove(entry)
    return manifest


def apply_manifest(project: Project, manifest: dict, args, label: str) -> int:
    """Preview or write a manifest change, then sync the project (with a fix session on failure)."""
    rel = f"projects/{project.id}.json"
    diff = list(difflib.unified_diff(json.dumps(project.manifest, indent=2).splitlines(),
                                     json.dumps(manifest, indent=2).splitlines(), rel, rel, lineterm=""))
    RESULT.update(project=project.id, diff=diff, applied=False)
    if not diff:
        print("nothing to change")
        return 0
    for line in diff:
        print(paint("32" if line[:1] == "+" else "31" if line[:1] == "-" else "2", line))
    planned = Project(project.id, manifest, project.repo)
    if not args.apply:
        logs = sync_project(planned, dry=True)
        print_logs(logs)
        RESULT["plan"] = [log.to_dict() for log in logs]
        print(paint("2", "preview: nothing was written; pass --apply"))
        return 0
    write_json(PROJECTS_DIR / f"{project.id}.json", manifest)
    kit_catalog.cache_clear()
    logs = sync_project(planned, dry=False)
    print_logs(logs)
    healthy = maybe_fix(planned, label, logs, args)
    RESULT.update(applied=True, healthy=healthy)
    return 0 if healthy else 1


def names(value: str | None) -> list[str]:
    return [n for n in (value or "").split(",") if n]


def cmd_skills(args) -> int:
    project = by_id(args.id)
    if args.action == "set":
        add, remove = names(args.add), names(args.remove)
        if not add and not remove:
            raise SystemExit("acp: pass --add and/or --remove")
        runtimes = names(args.runtimes) or default_runtimes(project)
        return apply_manifest(project, edit_skills(project, add, remove, runtimes), args, "skills set")
    ak = project.ak
    data = {"project": project.id, "runtimes": default_runtimes(project), "agentkit": None,
            "personal": {"catalog": personal_catalog(),
                         "selected": {s["name"]: s.get("runtimes", []) for s in project.manifest.get("skills", [])}}}
    if ak:
        catalog = list(kit_catalog(ak.get("kit", "engineer")))
        wanted = ak.get("skills", "all")
        selected = [s for s in catalog if s not in ak.get("exclude", [])] if wanted == "all" else wanted
        data["agentkit"] = {"kit": ak.get("kit", "engineer"), "mode": "all" if wanted == "all" else "list",
                            "exclude": ak.get("exclude", []), "selected": selected, "catalog": catalog}
        print(paint("1", f"AgentKit {data['agentkit']['kit']}: {len(selected)}/{len(catalog)} skills"
                         + (f", excluding {', '.join(ak['exclude'])}" if ak.get("exclude") else "")))
    for name, runtimes in data["personal"]["selected"].items():
        print(f"  personal {name} ({', '.join(runtimes)})")
    RESULT.update(data)
    return 0


def cmd_kit(args) -> int:
    project = by_id(args.id)
    if project.ak:
        raise SystemExit(f"acp: {project.id} already uses AgentKit {project.ak.get('kit', 'engineer')}")
    manifest = json.loads(json.dumps(project.manifest))
    manifest["agentkit"] = {"kit": args.kit, "channel": args.channel,
                            "targets": args.targets.split(","), "skills": "all"}
    return apply_manifest(project, manifest, args, "kit add")


# ----------------------------------------------------------------------- fix


SECRET_PATTERNS = (
    (re.compile(r"(?i)\b(api[_-]?key|token|secret|password|authorization)(\"?\s*[:=]\s*\"?)[^\s\"',]+"),
     r"\1\2<redacted>"),
    (re.compile(r"\bsk-[A-Za-z0-9_-]{16,}"), "<redacted>"),
    (re.compile(r"\bgh[pousr]_[A-Za-z0-9]{20,}"), "<redacted>"),
)
OP_ID = re.compile(r"^\d{8}-\d{6}-[0-9a-f]{4}$")


def redact(value):
    if isinstance(value, str):
        for pattern, replacement in SECRET_PATTERNS:
            value = pattern.sub(replacement, value)
        return value
    if isinstance(value, list):
        return [redact(v) for v in value]
    if isinstance(value, dict):
        return {k: redact(v) for k, v in value.items()}
    return value


def new_op() -> str:
    return time.strftime("%Y%m%d-%H%M%S-") + secrets.token_hex(2)


def fix_settings(overrides: dict | None = None) -> dict:
    settings = {"auto": True, "runner": "direct", "target": "claude-code", "attempts": 3, "timeout": 900}
    settings.update(load_json(SETTINGS, {}) or {})
    env = os.environ
    if env.get("ACP_FIX"):
        settings["auto"] = env["ACP_FIX"] != "off"
    for key in ("runner", "target", "attempts", "timeout"):
        value = env.get(f"ACP_FIX_{key.upper()}")
        if value:
            settings[key] = int(value) if key in ("attempts", "timeout") else value
    settings.update({k: v for k, v in (overrides or {}).items() if v is not None})
    return settings


def maybe_fix(project: Project, label: str, logs: list[Log], args) -> bool:
    """True when the project ends healthy; a failure starts a fix session unless disabled."""
    if not any(log.issues for log in logs):
        return True
    if getattr(args, "dry_run", False) or getattr(args, "no_fix", False) or not fix_settings()["auto"]:
        return False
    if not project.repo or not project.repo.is_dir():
        return False
    return run_fix(project, label, logs, fix_settings())


def file_hash(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def snapshot(path: Path) -> dict[str, str]:
    """Content map of a file or tree, without following links and skipping per-checkout state."""
    if path.is_symlink():
        return {"": "link:" + os.readlink(path)}
    if path.is_file():
        return {"": file_hash(path)}
    result: dict[str, str] = {}
    for folder, dirs, files in os.walk(path):
        base = Path(folder)
        for name in list(dirs):
            if name in MIRROR_SKIP or name == "__pycache__":
                dirs.remove(name)
            elif (base / name).is_symlink():
                dirs.remove(name)
                result[(base / name).relative_to(path).as_posix()] = "link:" + os.readlink(base / name)
        for name in files:
            if name in MIRROR_SKIP:
                continue
            item = base / name
            key = item.relative_to(path).as_posix()
            try:
                result[key] = "link:" + os.readlink(item) if item.is_symlink() else file_hash(item)
            except OSError:
                result[key] = "unreadable"
    return result


def remove_path(path: Path) -> None:
    if path.is_symlink() or path.is_file():
        path.unlink()
    elif path.is_dir():
        shutil.rmtree(path)


def fix_zones(project: Project, main: Path) -> list[Path]:
    """What a fix agent may change: agent folders of every checkout and the project's fleet records."""
    zones = [main / root for root in MIRROR_ROOTS]
    kit_root = main / project.ak.get("kit", "engineer")
    if (kit_root / ".codex").exists() or (kit_root / ".agentkit").exists():
        zones.append(kit_root)  # AgentKit 2.x Codex lifecycle root
    for worktree in worktrees(main):
        zones += [worktree / root for root in MIRROR_ROOTS]
    return zones + [PROJECTS_DIR / f"{project.id}.json", OVERLAY, LOCK]


def backup_zones(op: str, zones: list[Path]) -> dict:
    root = FIX_BACKUPS / op
    entries = []
    for index, zone in enumerate(zones):
        copy = root / "files" / str(index)
        kind = "link" if zone.is_symlink() else "dir" if zone.is_dir() else "file" if zone.is_file() else "absent"
        copy.parent.mkdir(parents=True, exist_ok=True)
        if kind == "dir":
            shutil.copytree(zone, copy, symlinks=True,
                            ignore=shutil.ignore_patterns(*MIRROR_SKIP, "__pycache__"))
        elif kind != "absent":
            shutil.copy2(zone, copy, follow_symlinks=False)
        entries.append({"path": str(zone), "kind": kind, "copy": f"files/{index}", "hashes": snapshot(zone)})
    manifest = {"op": op, "created": stamp(), "zones": entries}
    write_json(root / "manifest.json", manifest)
    return manifest


def zone_changes(manifest: dict) -> list[tuple[str, Path, Path]]:
    """(A|D|M, current path, backup path) for every path that differs from a backup."""
    root = FIX_BACKUPS / manifest["op"]
    changes = []
    for entry in manifest["zones"]:
        zone, copy = Path(entry["path"]), root / entry["copy"]
        before, after = entry["hashes"], snapshot(zone) if zone.exists() or zone.is_symlink() else {}
        for key in sorted(set(before) | set(after)):
            if before.get(key) != after.get(key):
                sign = "A" if key not in before else "D" if key not in after else "M"
                changes.append((sign, zone / key if key else zone, copy / key if key else copy))
    return changes


def write_diff(session: Path, manifest: dict) -> list[str]:
    lines, summary = [], []
    for sign, path, copy in zone_changes(manifest):
        summary.append(f"{sign} {path}")
        lines.append(f"{sign} {path}")
        if sign == "M" and path.is_file() and not path.is_symlink() and path.stat().st_size < 65536:
            try:
                old = copy.read_text(encoding="utf-8").splitlines()
                new = path.read_text(encoding="utf-8").splitlines()
            except UnicodeDecodeError:
                continue
            lines += difflib.unified_diff(old, new, "backup", "now", lineterm="")
    (session / "diff.txt").write_text(redact("\n".join(lines)) + "\n", encoding="utf-8")
    return summary


def git_changes(repo: Path) -> set[str]:
    listing = git(repo, "status", "--porcelain") if git(repo, "rev-parse", "--git-dir") else None
    return set((listing or "").splitlines())


def guard_state(main: Path) -> dict:
    return {"protected": {name: snapshot(ROOT / name) for name in PROTECTED}, "project": git_changes(main)}


def manifest_violations(before: dict, after: dict | None) -> list[str]:
    """Removing a skill, a target or AgentKit, or switching kit or channel, is the user's decision."""
    if after is None:
        return ["deleted the project manifest"]

    def pairs(manifest: dict) -> set[tuple[str, str]]:
        return {(s["name"], r) for s in manifest.get("skills", []) for r in s.get("runtimes", [])}

    problems = [f"removed personal skill {n} ({r})" for n, r in sorted(pairs(before) - pairs(after))]
    old, new = before.get("agentkit") or {}, after.get("agentkit") or {}
    if old and not new:
        return problems + ["removed AgentKit from the manifest"]
    if not old:
        return problems
    for key in ("kit", "channel"):
        if old.get(key, new.get(key)) != new.get(key, old.get(key)):
            problems.append(f"changed agentkit.{key} {old.get(key)} -> {new.get(key)}")
    problems += [f"removed target {t}" for t in sorted(set(old.get("targets", [])) - set(new.get("targets", [])))]
    was, now = old.get("skills", "all"), new.get("skills", "all")
    if was == "all" and now != "all":
        problems.append("narrowed agentkit.skills from all to a list")
    elif isinstance(was, list) and isinstance(now, list):
        problems += [f"removed AgentKit skill {s}" for s in sorted(set(was) - set(now))]
    problems += [f"excluded AgentKit skill {s}"
                 for s in sorted(set(new.get("exclude", [])) - set(old.get("exclude", [])))]
    return problems


def guard_violations(project: Project, before: dict, after: dict,
                     manifest_before: dict, manifest_after: dict | None) -> list[str]:
    problems = []
    for name in PROTECTED:
        old, new = before["protected"][name], after["protected"][name]
        changed = sorted(k for k in set(old) | set(new) if old.get(k) != new.get(k))
        if changed:
            problems.append(f"changed control-plane {name}/: {', '.join(changed[:5])}")
    allowed = {*MIRROR_ROOTS, project.ak.get("kit", "engineer")}
    for line in sorted(after["project"] - before["project"]):
        path = line[3:].split(" -> ")[-1].strip('"')
        if path.split("/")[0] not in allowed:
            problems.append(f"changed a project file outside the agent folders: {path}")
    return problems + manifest_violations(manifest_before, manifest_after)


def tool_bin(name: str) -> str:
    found = os.environ.get(f"ACP_{name.upper()}") or shutil.which(name)
    if not found:
        raise SystemExit(f"acp: `{name}` is not on PATH; pick another fix target (`acp fix config`)")
    return found


FIX_PROMPT = """\
An `acp {label}` run failed for the fleet project `{project}` (main checkout {main}).
Find the cause and fix it so that `acp sync {project}` succeeds.

Evidence: {evidence}
Read the evidence first: the failure, the full `ak` output, project status, manifest and lock.
{skill}
You may change only these paths (a backup was taken; the user can roll back):
{zones}

Never change {protected}, the ak CLI or any other file of the project.
Never remove a skill (a personal skill, an `agentkit.skills` entry, an `agentkit.exclude` addition),
a target or AgentKit from a manifest, and never change `agentkit.kit` or `agentkit.channel`.
Never pass --force or --global, and never run git push, commit, reset, stash or checkout.
Do not re-run `acp sync`, `acp upgrade` or `acp skills set`: acp re-runs the sync after you finish.

When you are done, write {diagnosis} as JSON:
{{"cause": "...", "cause_location": "runtime|project-native|manifest|control-plane|ak|unknown",
  "fix": "what you changed", "changed": ["paths"], "needs_user": null}}
If the cause is in control-plane code or in ak itself, or the fix needs a forbidden change,
change nothing, explain it in "needs_user" and stop.
{history}"""


def fix_prompt(project: Project, label: str, main: Path, session: Path, adir: Path, zones: list[Path],
               attempts: list[dict]) -> str:
    skill = next((p for p in (main / ".claude/skills/ak-fix/SKILL.md", main / ".agents/skills/ak-fix/SKILL.md")
                  if p.is_file()), None)
    history = "".join(f"\nAttempt {a['attempt']} did not fix it: {a.get('diagnosis', {}).get('fix') or 'no diagnosis'};"
                      f" the re-run still reported: {'; '.join(a['issues'][:3])}" for a in attempts)
    return FIX_PROMPT.format(
        label=label, project=project.id, main=main,
        evidence=session / "evidence.json", diagnosis=adir / "diagnosis.json",
        skill=f"Follow the workflow in {skill}.\n" if skill else "",
        zones="\n".join(f"- {z}" for z in zones),
        protected=", ".join(f"{ROOT / n}/" for n in PROTECTED), history=history)


def agent_command(settings: dict, project: Project, main: Path, prompt: str, adir: Path) -> list[str]:
    target = settings["target"]
    if settings["runner"] == "ak-run":
        return [ak_bin(), "run", f"{project.ak.get('kit', 'engineer')}/fix", "--target", target, "--json",
                "--timeout", f"{settings['timeout']}s", "--yes", "--no-interactive", prompt]
    if target == "codex":
        return [tool_bin("codex"), "exec", "--json", "-C", str(main), "-s", "workspace-write",
                "--add-dir", str(ROOT), "--skip-git-repo-check", "-o", str(adir / "last-message.txt"), prompt]
    denied = [f"{tool}(//{(ROOT / name).as_posix().lstrip('/')}/**)" for tool in ("Edit", "Write") for name in PROTECTED]
    denied += [f"Bash({c}:*)" for c in ("git push", "git commit", "git reset", "git stash", "git checkout", "rm -rf")]
    return [tool_bin("claude"), "-p", prompt, "--output-format", "stream-json", "--verbose",
            "--permission-mode", "acceptEdits", "--permission-prompts", "none", "--add-dir", str(ROOT),
            "--allowedTools", "Read,Edit,Write,Glob,Grep,Bash", "--disallowedTools", ",".join(denied)]


def summarize_event(line: str) -> str | None:
    """One readable line from a Claude stream-json, Codex --json or ak run NDJSON event."""
    try:
        data = json.loads(line)
    except ValueError:
        return line.strip() or None
    if not isinstance(data, dict):
        return None
    message = data.get("message")
    for part in (message.get("content") or []) if isinstance(message, dict) else []:
        if isinstance(part, dict) and part.get("type") == "text" and part.get("text", "").strip():
            return part["text"].strip()
        if isinstance(part, dict) and part.get("type") == "tool_use":
            return f"{part.get('name')}: {json.dumps(part.get('input'))[:200]}"
    if data.get("type") == "result" and data.get("result"):
        return str(data["result"]).strip()
    item = data.get("item")
    if isinstance(item, dict):
        if item.get("command"):
            return f"$ {item['command']}"
        if isinstance(item.get("text"), str) and item["text"].strip():
            return item["text"].strip()
    for key in ("text", "message", "error"):
        if isinstance(data.get(key), str) and data[key].strip():
            return data[key].strip()
    return None


def run_agent(command: list[str], cwd: Path, adir: Path, timeout: int) -> int:
    events = (adir / "events.ndjson").open("a", encoding="utf-8")
    transcript = (adir / "transcript.ndjson").open("w", encoding="utf-8")

    def emit(kind: str, text: str) -> None:
        record = {"ts": stamp(), "event": kind, "text": redact(text)[:4000]}
        events.write(json.dumps(record) + "\n")
        events.flush()
        print(f"    {paint('2', kind)} {record['text'].splitlines()[0][:160] if record['text'] else ''}", flush=True)

    emit("started", f"{Path(command[0]).name} {command[1]} (timeout {timeout}s)")
    # A fix agent that calls acp must not start a fix session of its own.
    env = {**os.environ, "ACP_FIX": "off"}
    proc = subprocess.Popen(command, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                            stdin=subprocess.DEVNULL, text=True)
    expired = threading.Event()

    def expire() -> None:
        expired.set()
        proc.kill()

    timer = threading.Timer(timeout, expire)
    timer.start()
    last = None
    try:
        for line in proc.stdout:
            transcript.write(redact(line))
            transcript.flush()
            text = summarize_event(line)
            if text and text != last:  # Claude repeats its final message as the result event
                emit("log", text)
                last = text
        code = proc.wait()
    finally:
        timer.cancel()
    code = 124 if expired.is_set() else code
    emit("finished", f"exit {code}" + (" (timed out)" if expired.is_set() else ""))
    events.close()
    transcript.close()
    return code


def failure_record(logs: list[Log]) -> list[dict]:
    return [{**log.to_dict(), "details": log.details} for log in logs if log.issues]


def write_fix_report(result: dict, evidence: dict) -> Path:
    REPORTS.mkdir(parents=True, exist_ok=True)
    path = REPORTS / f"acp-fix-{result['op']}-{result['project']}.md"
    issues = [i for f in evidence["failure"] for i in f["issues"]]
    diagnosis = result.get("diagnosis") or {}
    path.write_text(redact("\n".join([
        f"# acp fix {result['op']}: {result['project']} ({result['state']})", "",
        f"`acp {result['command']}` failed and the {result['target']} agent ({result['runner']}) "
        f"stopped after {result['attempt']} attempt(s): {result['reason']}.", "",
        "## Failure", "", *[f"- {i}" for i in issues], "",
        "## Agent diagnosis", "",
        f"- Cause: {diagnosis.get('cause') or 'not recorded'}",
        f"- Where: {diagnosis.get('cause_location') or 'not recorded'}",
        f"- Needs from you: {diagnosis.get('needs_user') or 'nothing recorded'}", "",
        "## Recover", "",
        f"- Changes the agent made: `acp fix show {result['op']}` (diff in {FIX_SESSIONS / result['op']}/diff.txt)",
        f"- Undo them: `acp fix rollback {result['op']} --yes`", ""])), encoding="utf-8")
    return path


def run_fix(project: Project, label: str, logs: list[Log], settings: dict) -> bool:
    main = project.repo.resolve()
    op = new_op()
    session = FIX_SESSIONS / op
    session.mkdir(parents=True)
    zones = fix_zones(project, main)
    manifest_path = PROJECTS_DIR / f"{project.id}.json"
    evidence = {"op": op, "project": project.id, "repo": str(main), "command": label,
                "failure": failure_record(logs), "status": project_status(project),
                "manifest": project.manifest, "lock": load_json(LOCK, {}),
                "allowed_paths": [str(z) for z in zones], "protected_paths": [str(ROOT / n) for n in PROTECTED]}
    write_json(session / "evidence.json", redact(evidence))
    result = {"op": op, "project": project.id, "command": label, "runner": settings["runner"],
              "target": settings["target"], "web_op": os.environ.get("ACP_WEB_OP"), "state": "running",
              "attempt": 0, "attempts": settings["attempts"], "started": stamp(), "reason": "",
              "backup": str(FIX_BACKUPS / op)}
    write_json(session / "result.json", result)
    print(paint("1", f"fix {op}: `{label}` failed for {project.id}; "
                     f"{settings['target']} ({settings['runner']}) takes over"), flush=True)
    backup = backup_zones(op, zones)
    history: list[dict] = []
    for attempt in range(1, settings["attempts"] + 1):
        adir = session / f"attempt-{attempt}"
        adir.mkdir()
        result["attempt"] = attempt
        write_json(session / "result.json", result)
        print(f"  attempt {attempt}/{settings['attempts']}", flush=True)
        before, manifest_before = guard_state(main), load_json(manifest_path)
        prompt = fix_prompt(project, label, main, session, adir, zones, history)
        (adir / "prompt.txt").write_text(prompt, encoding="utf-8")
        try:
            command = agent_command(settings, project, main, prompt, adir)
        except SystemExit as missing:
            result.update(state="gave-up", reason=str(missing.code).removeprefix("acp: "))
            break
        code = run_agent(command, main, adir, settings["timeout"])
        manifest_after = load_json(manifest_path)
        problems = guard_violations(project, before, guard_state(main), manifest_before, manifest_after)
        if problems:
            if manifest_violations(manifest_before, manifest_after):
                write_json(manifest_path, manifest_before)  # skills, targets and channels stay the user's call
            result.update(state="violated", reason="; ".join(problems))
            break
        diagnosis = load_json(adir / "diagnosis.json", {}) or {}
        result["diagnosis"] = diagnosis
        if diagnosis.get("needs_user") or diagnosis.get("cause_location") in ("control-plane", "ak"):
            result.update(state="gave-up",
                          reason=diagnosis.get("needs_user") or f"the cause is in {diagnosis['cause_location']}")
            break
        fresh = by_id(project.id)
        retry = sync_project(fresh, dry=False)
        print_logs(retry)
        issues = [i for log in retry for i in log.issues]
        if not issues and project_status(fresh)["healthy"]:
            result.update(state="fixed", reason=diagnosis.get("cause") or "the re-run passed")
            break
        history.append({"attempt": attempt, "agent_exit": code, "diagnosis": diagnosis,
                        "issues": issues or project_status(fresh)["notes"]})
        evidence["failure"], evidence["attempts"] = failure_record(retry), history
        write_json(session / "evidence.json", redact(evidence))
    else:
        result.update(state="gave-up", reason=f"still failing after {settings['attempts']} attempt(s)")
    result.update(finished=stamp(), changed=write_diff(session, backup))
    if result["state"] != "fixed":
        result["report"] = str(write_fix_report(result, evidence))
    write_json(session / "result.json", result)
    colour = "32" if result["state"] == "fixed" else "31"
    print(paint(colour, f"fix {op}: {result['state']} ({result['reason']}); "
                        f"{len(result['changed'])} path(s) changed, undo with `acp fix rollback {op} --yes`"))
    RESULT.setdefault("fix_sessions", []).append(result)
    return result["state"] == "fixed"


def fix_session(op: str) -> Path:
    if not OP_ID.match(op) or not (FIX_SESSIONS / op).is_dir():
        raise SystemExit(f"acp: no fix session '{op}' (see `acp fix list`)")
    return FIX_SESSIONS / op


def rollback(op: str, apply: bool) -> int:
    fix_session(op)
    manifest = load_json(FIX_BACKUPS / op / "manifest.json")
    if not manifest:
        raise SystemExit(f"acp: fix session {op} has no backup")
    changes = zone_changes(manifest)
    signs = {"A": "-", "D": "+", "M": "~"}
    for sign, path, _ in changes:
        print(f"  {signs[sign]} {path}")
    RESULT.update(op=op, changes=[f"{signs[s]} {p}" for s, p, _ in changes], applied=False)
    if not changes:
        print("nothing to roll back: every backed-up path matches the backup")
        return 0
    if not apply:
        print(paint("2", f"dry run: pass --yes to restore {len(changes)} path(s)"))
        return 0
    root = FIX_BACKUPS / op
    for entry in manifest["zones"]:
        zone, copy, hashes = Path(entry["path"]), root / entry["copy"], entry["hashes"]
        if entry["kind"] != "dir":
            remove_path(zone)
            if entry["kind"] != "absent":
                zone.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(copy, zone, follow_symlinks=False)
            continue
        if zone.is_symlink() or zone.is_file():
            zone.unlink()
        current = snapshot(zone) if zone.is_dir() else {}
        for key, value in current.items():
            if hashes.get(key) != value and ((zone / key).exists() or (zone / key).is_symlink()):
                remove_path(zone / key)
        for key, value in hashes.items():
            if current.get(key) == value and ((zone / key).exists() or (zone / key).is_symlink()):
                continue
            dest = zone / key
            if dest.exists() or dest.is_symlink():
                remove_path(dest)
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(copy / key, dest, follow_symlinks=False)
    left = zone_changes(manifest)
    RESULT.update(applied=True, verified=not left)
    print(paint("32", "restored: every backed-up path matches the backup") if not left
          else paint("31", f"{len(left)} path(s) still differ from the backup"))
    return 0 if not left else 1


def cmd_fix(args) -> int:
    if args.action == "run":
        project = selected(args)[0]
        settings = fix_settings({"runner": args.runner, "target": args.target, "attempts": args.attempts})
        if args.dry_run:
            main = project.repo.resolve()
            plan = {"settings": settings, "allowed_paths": [str(z) for z in fix_zones(project, main)],
                    "command": shlex.join(agent_command(settings, project, main, "<prompt>",
                                                        FIX_SESSIONS / "<op>")[:4]) + " ..."}
            RESULT.update(project=project.id, plan=plan)
            print(json.dumps(plan, indent=2))
            print(paint("2", "dry run: no sync, no backup, no agent"))
            return 0
        logs = sync_project(project, dry=False)
        print_logs(logs)
        if not any(log.issues for log in logs):
            print(f"{project.id} syncs cleanly; nothing to fix")
            return 0
        return 0 if run_fix(project, "sync", logs, settings) else 1
    if args.action == "list":
        sessions = []
        for path in sorted(FIX_SESSIONS.glob("*/result.json"), reverse=True):
            result = load_json(path, {}) or {}
            if args.web_op and result.get("web_op") != args.web_op:
                continue
            sessions.append(result)
        for s in sessions[: args.limit]:
            print(f"{s.get('op')}  {s.get('project', ''):<20} {s.get('state', ''):<9} "
                  f"{s.get('attempt')}/{s.get('attempts')}  {s.get('reason', '')[:80]}")
        RESULT.update(sessions=sessions[: args.limit], settings=fix_settings())
        return 0
    if args.action == "show":
        session = fix_session(args.op)
        result = load_json(session / "result.json", {}) or {}
        events = []
        for attempt in sorted(session.glob("attempt-*/events.ndjson")):
            events += [json.loads(line) for line in attempt.read_text(encoding="utf-8").splitlines() if line]
        diff = (session / "diff.txt").read_text(encoding="utf-8") if (session / "diff.txt").exists() else ""
        RESULT.update(result=result, events=events[-200:], diff=diff)
        if args.transcript:
            RESULT["transcript"] = "".join(p.read_text(encoding="utf-8")
                                           for p in sorted(session.glob("attempt-*/transcript.ndjson")))
        print(json.dumps(result, indent=2))
        for event in events[-20:]:
            print(f"  {event['event']}: {event['text'].splitlines()[0][:160] if event['text'] else ''}")
        return 0
    if args.action == "rollback":
        return rollback(args.op, args.yes)
    # config
    changes = {k: v for k, v in (("runner", args.runner), ("target", args.target), ("attempts", args.attempts),
                                 ("timeout", args.timeout)) if v is not None}
    if args.auto:
        changes["auto"] = args.auto == "on"
    if changes:
        write_json(SETTINGS, {**(load_json(SETTINGS, {}) or {}), **changes})
    settings = fix_settings()
    RESULT.update(settings=settings)
    print(json.dumps(settings, indent=2))
    return 0


# --------------------------------------------------------------------- setup


def ak_path() -> str | None:
    local = Path(os.environ.get("AK_INSTALL_DIR") or Path.home() / ".local/bin") / "ak"
    return os.environ.get("ACP_AK") or shutil.which("ak") or (str(local) if local.is_file() else None)


def ak_json(binary: str, *args: str) -> dict:
    try:
        result = subprocess.run([binary, *args, "--json"], capture_output=True, text=True, timeout=120)
        return json.loads(result.stdout or "{}").get("data") or {}
    except (OSError, ValueError, subprocess.TimeoutExpired):
        return {}


def fleet_channel() -> str:
    channels = Counter(p.ak.get("channel", "beta") for p in fleet() if p.ak)
    return channels.most_common(1)[0][0] if channels else "beta"


def cmd_setup(args) -> int:
    if args.action == "install-ak":
        return install_ak(args)
    binary = ak_path()
    if args.action == "login":
        if not binary:
            raise SystemExit("acp: install ak first: `acp setup install-ak --apply`")
        if not sys.stdin.isatty() or not TTY:
            raise SystemExit("acp: sign-in is interactive; run `ak onboard` in a terminal")
        return subprocess.run([binary, "onboard"]).returncode
    info = {"ak": {"path": binary, "version": None}, "login": None, "healthy": None,
            "pins": (load_json(LOCK, {}) or {}).get("channels", {}), "channel": fleet_channel()}
    if binary:
        version = subprocess.run([binary, "--version"], capture_output=True, text=True)
        info["ak"]["version"] = (version.stdout.strip().split() or [None])[-1]
        whoami = ak_json(binary, "whoami")
        info["login"] = {"authenticated": bool(whoami.get("authenticated")), "method": whoami.get("authMethod")}
        info["healthy"] = ak_json(binary, "doctor").get("healthy")
    RESULT.update(info)
    if not binary:
        print(f"ak       {paint('31', 'not installed')}  ->  acp setup install-ak --apply")
        return 1
    login = info["login"]
    print(f"ak       {info['ak']['version']}  ({binary})")
    print(f"login    {paint('32', 'signed in (' + str(login['method']) + ')') if login['authenticated'] else paint('31', 'not signed in -> acp setup login')}")
    print(f"doctor   {paint('32', 'healthy') if info['healthy'] else paint('33', 'needs attention: ak doctor')}")
    print(paint("2", "pinned   " + ", ".join(f"{c}/{k} {v}" for c, kits in info["pins"].items() for k, v in kits.items())))
    return 0 if login["authenticated"] and info["healthy"] else 1


def install_ak(args) -> int:
    """Run AgentKit's official installer; it verifies the release before installing."""
    channel = args.channel or fleet_channel()
    env = {"AK_CHANNEL": channel, **({"AK_VERSION": args.version} if args.version else {})}
    urls = [os.environ["ACP_INSTALLER_URL"]] if os.environ.get("ACP_INSTALLER_URL") else list(INSTALLERS)
    target = Path(os.environ.get("AK_INSTALL_DIR") or Path.home() / ".local/bin") / "ak"
    RESULT.update(installer=urls, env=env, binary=str(target), applied=False)
    print(f"download {urls[0]}" + (f" (fallback {', '.join(urls[1:])})" if urls[1:] else ""))
    print(f"run      {' '.join(f'{k}={v}' for k, v in env.items())} sh install.sh  ->  {target}")
    print("verify   ak --version; ak doctor --exit-on-fail")
    if not args.apply:
        print(paint("2", "preview: nothing was downloaded; pass --apply"))
        return 0
    folder = ROOT / "runtime" / "installers" / time.strftime("%Y%m%d-%H%M%S")
    folder.mkdir(parents=True, exist_ok=True)
    script = folder / "install.sh"
    for url in urls:
        if subprocess.run(["curl", "-fsSL", "--retry", "2", url, "-o", str(script)]).returncode == 0:
            break
    else:
        raise SystemExit("acp: could not download the AgentKit installer from any mirror")
    RESULT.update(applied=True, source=url, sha256=file_hash(script))
    print(f"installer sha256 {RESULT['sha256']}")
    run = subprocess.run(["sh", str(script)], env={**os.environ, **env}, capture_output=True, text=True)
    print((run.stdout + run.stderr).rstrip())
    if run.returncode != 0:
        print(paint("31", f"the installer refused or failed (exit {run.returncode}); nothing else was tried"))
        return 1
    version = subprocess.run([str(target), "--version"], capture_output=True, text=True)
    doctor = subprocess.run([str(target), "doctor", "--exit-on-fail"], capture_output=True, text=True)
    RESULT.update(version=version.stdout.strip(), doctor_ok=doctor.returncode == 0)
    print(f"installed {version.stdout.strip()}; doctor {'ok' if doctor.returncode == 0 else 'failed'}")
    if not shutil.which("ak"):
        print(paint("33", f"{target.parent} is not on PATH; open a new terminal or add it"))
    return 0 if doctor.returncode == 0 else 1


# ------------------------------------------------------------------ describe


READ = {"mutation": False, "risk": "read"}
COMMANDS = {
    "status": {**READ, "summary": "fleet status: AgentKit per target, personal links, worktrees"},
    "doctor": {**READ, "summary": "fleet status; exit 1 when anything needs a sync"},
    "sync": {"summary": "install or refresh AgentKit and links; a failure starts a fix session",
             "mutation": True, "risk": "medium", "preview": "--dry-run", "network": True, "agent": True},
    "link": {"summary": "offline link of one checkout or worktree", "mutation": True, "risk": "low",
             "preview": "--check"},
    "unlink": {"summary": "remove the links acp made in one checkout", "mutation": True, "risk": "low",
               "preview": "--dry-run"},
    "upgrade": {"summary": "update ak, pin the channel's latest kit, sync the fleet", "mutation": True,
                "risk": "high", "preview": "--dry-run", "network": True, "agent": True},
    "add": {"summary": "add a repo to the fleet and sync it", "mutation": True, "risk": "medium",
            "preview": "--dry-run", "agent": True},
    "remove": {"summary": "take a repo out of the fleet", "mutation": True, "risk": "medium",
               "preview": "default; dry run until --yes", "confirm": "--yes"},
    "activity": {**READ, "summary": "recent Claude/Codex sessions per repo"},
    "global": {"summary": "link global personal skills into user homes", "mutation": True, "risk": "low",
               "preview": "--check"},
    "hooks": {"summary": "session-start hooks that link worktrees", "mutation": True, "risk": "low",
              "preview": "--dry-run (or the status action)"},
    "skills list": {**READ, "summary": "a project's AgentKit and personal skills with their catalogs"},
    "skills set": {"summary": "add or remove skills of a project", "mutation": True, "risk": "medium",
                   "preview": "default; preview until --apply", "confirm": "--apply", "agent": True},
    "kit add": {"summary": "give a project AgentKit", "mutation": True, "risk": "medium",
                "preview": "default; preview until --apply", "confirm": "--apply", "network": True, "agent": True},
    "fix run": {"summary": "sync one project and hand a failure to a coding agent", "mutation": True,
                "risk": "high", "preview": "--dry-run", "agent": True},
    "fix list": {**READ, "summary": "fix sessions, newest first, with the fix settings"},
    "fix show": {**READ, "summary": "one fix session: result, agent events, diff, optional transcript"},
    "fix rollback": {"summary": "restore what a fix session backed up", "mutation": True, "risk": "medium",
                     "preview": "default; dry run until --yes", "confirm": "--yes"},
    "fix config": {"summary": "fix runner, target, attempts, timeout and auto mode", "mutation": True,
                   "risk": "low", "preview": "no flags; prints the current settings"},
    "setup": {**READ, "summary": "ak binary, sign-in, doctor and pins", "network": True},
    "setup install-ak": {"summary": "download and run AgentKit's official verified installer",
                         "mutation": True, "risk": "high", "preview": "default; plan until --apply",
                         "confirm": "--apply", "network": True},
    "setup login": {"summary": "sign in with `ak onboard` (terminal only)", "mutation": True, "risk": "medium",
                    "preview": "interactive; ak onboard confirms each step", "interactive": True,
                    "network": True},
    "describe": {**READ, "summary": "this contract"},
    "init": {"summary": "create a control plane from the template and record it as the default",
             "mutation": True, "risk": "low", "preview": "--dry-run"},
    "mcp": {"summary": "MCP server on stdio exposing acp's commands as tools", "mutation": False,
            "risk": "low", "server": True},
    "web": {"summary": "local UI on 127.0.0.1 with a per-run token; runs only allowlisted acp commands",
            "mutation": False, "risk": "low", "server": True},
}


def cmd_describe(args) -> int:
    name = " ".join(args.name)
    if name and name not in COMMANDS:
        raise SystemExit(f"acp: no command '{name}'")
    names = [name] if name else list(COMMANDS)
    commands = [{"name": n, "mutation": False, "risk": "read", "preview": None, "confirm": None,
                 "network": False, "agent": False, "json": True, **COMMANDS[n]} for n in names]
    RESULT.update(schema="acp.describe/1", envelope={"schema_version": 1, "kind": "acp.<command>",
                                                     "data": "object", "error": "string on failure"},
                  commands=commands)
    for c in commands:
        flags = "mutation " + c["risk"] if c["mutation"] else "read"
        print(f"{c['name']:<17} {flags:<16} {c['summary']}" + (f"  [preview: {c['preview']}]" if c["preview"] else ""))
    return 0


# ---------------------------------------------------------------------- init


def cmd_init(args) -> int:
    target = args.dir.expanduser().resolve()
    if target.exists() and any(target.iterdir()):
        raise SystemExit(f"acp: {target} is not empty; pick a new folder")
    recorded = None if args.no_default else CONFIG
    if args.dry_run:
        files = sorted(str(f.relative_to(TEMPLATE)).replace("gitignore", ".gitignore")
                       for f in TEMPLATE.rglob("*") if f.is_file() and f.name != ".gitkeep")
        RESULT.update(root=str(target), files=files, config=str(recorded) if recorded else None)
        print(f"would create {target} with: " + ", ".join(files))
        if recorded:
            print(f"would record it as the default root in {CONFIG}")
        return 0
    shutil.copytree(TEMPLATE, target, dirs_exist_ok=True,
                    ignore=shutil.ignore_patterns("__pycache__", ".DS_Store"))
    for keep in target.rglob(".gitkeep"):
        keep.unlink()
    (target / "gitignore").rename(target / ".gitignore")  # shipped without the dot so packaging keeps it
    if recorded:
        write_json(CONFIG, {**(load_json(CONFIG, {}) or {}), "root": str(target)})
    RESULT.update(root=str(target), config=str(recorded) if recorded else None)
    print(f"created control plane {target}")
    if recorded:
        print(f"default root recorded in {CONFIG}")
    print("next: `acp setup` to check ak, then `acp add <repo>` for each project, then `acp web`")
    return 0


def cmd_mcp(args) -> int:
    from acp.mcp_server import serve
    return serve()


# ----------------------------------------------------------------------- web


NAME = re.compile(r"^[a-z0-9][a-z0-9._-]{0,80}$")


def web_value(payload: dict, key: str, pattern: re.Pattern = NAME) -> str:
    value = payload.get(key)
    if not isinstance(value, str) or not pattern.match(value):
        raise ValueError(f"invalid {key}")
    return value


def web_skills(payload: dict) -> list[str]:
    argv = []
    for key in ("add", "remove"):
        names = payload.get(key) or []
        if not isinstance(names, list) or not all(isinstance(n, str) and NAME.match(n) for n in names):
            raise ValueError(f"invalid {key}")
        if names:
            argv += [f"--{key}", ",".join(names)]
    if not argv:
        raise ValueError("nothing to add or remove")
    return argv


def web_choice(payload: dict, key: str, choices: tuple[str, ...]) -> str:
    if payload.get(key) not in choices:
        raise ValueError(f"invalid {key}")
    return payload[key]


# kind -> argv builder; the web UI can run nothing else.
WEB_RUN = {
    "sync": lambda p: ["sync", web_value(p, "id")],
    "sync.adopt": lambda p: ["sync", web_value(p, "id"), "--adopt"],
    "skills.preview": lambda p: ["skills", "set", web_value(p, "id"), *web_skills(p)],
    "skills.apply": lambda p: ["skills", "set", web_value(p, "id"), *web_skills(p), "--apply"],
    "kit.preview": lambda p: ["kit", "add", web_value(p, "id")],
    "kit.apply": lambda p: ["kit", "add", web_value(p, "id"), "--apply"],
    "fix.run": lambda p: ["fix", "run", web_value(p, "id")],
    "fix.rollback.preview": lambda p: ["fix", "rollback", web_value(p, "op", OP_ID)],
    "fix.rollback": lambda p: ["fix", "rollback", web_value(p, "op", OP_ID), "--yes"],
    "fix.config": lambda p: ["fix", "config", "--runner", web_choice(p, "runner", ("direct", "ak-run")),
                             "--target", web_choice(p, "target", tuple(TARGETS))],
    "setup.install-ak.preview": lambda p: ["setup", "install-ak"],
    "setup.install-ak": lambda p: ["setup", "install-ak", "--apply"],
    "upgrade.preview": lambda p: ["upgrade", "--dry-run"],
    "upgrade": lambda p: ["upgrade"],
}


def web_read(path: str) -> list[str] | None:
    fixed = {"/api/status": ["status"], "/api/describe": ["describe"], "/api/setup": ["setup"],
             "/api/fix": ["fix", "list"]}
    if path in fixed:
        return fixed[path]
    parts = path.strip("/").split("/")
    if len(parts) == 3 and parts[:2] == ["api", "skills"] and NAME.match(parts[2]):
        return ["skills", "list", parts[2]]
    if len(parts) == 3 and parts[:2] == ["api", "plan"] and NAME.match(parts[2]):
        return ["sync", parts[2], "--dry-run"]
    if len(parts) in (3, 4) and parts[:2] == ["api", "fix"] and OP_ID.match(parts[2]):
        return ["fix", "show", parts[2]] + (["--transcript"] if parts[3:] == ["transcript"] else [])
    return None


def acp_json(argv: list[str], env: dict | None = None) -> tuple[int, dict]:
    result = subprocess.run([sys.executable, "-m", "acp", *argv, "--json"], capture_output=True, text=True,
                            env={**os.environ, "ACP_ROOT": str(ROOT), **(env or {})})
    try:
        return result.returncode, json.loads(result.stdout)
    except ValueError:
        return result.returncode, {"schema_version": 1, "kind": "acp." + ".".join(argv[:2]),
                                   "error": (result.stderr or result.stdout).strip()[-2000:]}


def cmd_web(args) -> int:
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

    token = secrets.token_urlsafe(24)
    ops: dict[str, dict] = {}
    busy = threading.Lock()
    hosts: set[str] = set()

    class Handler(BaseHTTPRequestHandler):
        server_version = "acp-web"

        def log_message(self, *_):
            pass

        def send(self, code: int, body: bytes, kind: str = "application/json") -> None:
            self.send_response(code)
            self.send_header("Content-Type", kind)
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Frame-Options", "DENY")
            self.send_header("X-Content-Type-Options", "nosniff")
            if kind.startswith("text/html"):
                self.send_header("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; "
                                 "style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; "
                                 "font-src 'self'")
            self.end_headers()
            self.wfile.write(body)

        def reply(self, code: int, data: dict) -> None:
            self.send(code, json.dumps(data).encode())

        def allowed(self, api: bool) -> bool:
            origin = self.headers.get("Origin")
            if self.headers.get("Host") not in hosts or (origin and origin.removeprefix("http://") not in hosts):
                self.reply(403, {"error": "forbidden host or origin"})
                return False
            if api and not hmac.compare_digest(self.headers.get("Authorization", ""), f"Bearer {token}"):
                self.reply(401, {"error": "missing or wrong token"})
                return False
            return True

        def do_GET(self):
            path = self.path.split("?")[0]
            if path == "/":
                if self.allowed(api=False):
                    self.send(200, WEB_PAGE.read_bytes(), "text/html; charset=utf-8")
                return
            if path == "/guide":
                if self.allowed(api=False):
                    self.send_response(301)
                    self.send_header("Location", "/guide/")
                    self.end_headers()
                return
            if path.startswith("/guide/"):
                if self.allowed(api=False):
                    name = path.removeprefix("/guide/") or "index.html"
                    kind = next((k for pattern, k in GUIDE_FILES.items() if re.fullmatch(pattern, name)), None)
                    found = next((d / name for d in GUIDE_DIRS if kind and (d / name).is_file()), None)
                    if found:
                        self.send(200, found.read_bytes(), kind)
                    else:
                        self.reply(404, {"error": "not found"})
                return
            if not self.allowed(api=True):
                return
            if path.startswith("/api/ops/"):
                record = ops.get(path.removeprefix("/api/ops/"))
                if not record:
                    self.reply(404, {"error": "no such operation"})
                    return
                _, fixes = acp_json(["fix", "list", "--web-op", record["op"]])
                self.reply(200, {**record, "fix_sessions": (fixes.get("data") or {}).get("sessions", [])})
                return
            argv = web_read(path)
            if argv is None:
                self.reply(404, {"error": "not found"})
                return
            code, envelope = acp_json(argv)
            self.reply(200 if "error" not in envelope else 400, envelope)

        def do_POST(self):
            if not self.allowed(api=True):
                return
            if self.path != "/api/run":
                self.reply(404, {"error": "not found"})
                return
            try:
                length = int(self.headers.get("Content-Length") or 0)
                payload = json.loads(self.rfile.read(min(length, 65536)) or b"{}")
                builder = WEB_RUN.get(payload.get("kind"))
                if not builder:
                    raise ValueError("unknown kind")
                argv = builder(payload.get("args") or {})
            except (ValueError, AttributeError) as error:
                self.reply(400, {"error": str(error)})
                return
            if not busy.acquire(blocking=False):
                self.reply(409, {"error": "another operation is running"})
                return
            op = new_op()
            record = {"op": op, "kind": payload["kind"], "argv": argv, "state": "running", "started": stamp()}
            ops[op] = record

            def work():
                try:
                    code, envelope = acp_json(argv, {"ACP_WEB_OP": op})
                    record.update(exit=code, envelope=envelope, finished=stamp(),
                                  state="done" if code == 0 else "failed")
                finally:
                    busy.release()

            threading.Thread(target=work, daemon=True).start()
            self.reply(202, record)

    try:
        server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    except OSError as error:
        raise SystemExit(f"acp: cannot listen on 127.0.0.1:{args.port} ({error.strerror}); "
                         f"another `acp web` may be running: lsof -i :{args.port}")
    port = server.server_address[1]
    hosts.update({f"127.0.0.1:{port}", f"localhost:{port}"})
    url = f"http://127.0.0.1:{port}/#token={token}"
    print(f"acp web: {url}", flush=True)
    if not args.no_open:
        import webbrowser
        webbrowser.open(url)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


# ---------------------------------------------------------------------- main


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="acp", description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--json", action="store_true", help="print {schema_version, kind, data}")
    common = argparse.ArgumentParser(add_help=False)
    common.add_argument("--json", action="store_true", default=argparse.SUPPRESS,
                        help="print {schema_version, kind, data}")
    sub = parser.add_subparsers(dest="command")

    def command(name: str, **kwargs) -> argparse.ArgumentParser:
        return sub.add_parser(name, parents=[common], **kwargs)  # every command takes --json

    def no_fix(p: argparse.ArgumentParser) -> None:
        p.add_argument("--no-fix", action="store_true", help="on failure, do not start a fix session")

    for name in ("status", "doctor"):
        p = command(name, help="fleet status" if name == "status" else "status, exit 1 on drift")
        p.add_argument("-v", "--verbose", action="store_true")

    p = command("sync", help="sync projects (default: the one you are in)")
    p.add_argument("ids", nargs="*")
    p.add_argument("--all", action="store_true")
    p.add_argument("--dry-run", action="store_true")
    p.add_argument("--offline", action="store_true", help="skip AgentKit; only links")
    p.add_argument("--adopt", action="store_true",
                   help="back up old agent-file copies in worktrees and link them to main")
    no_fix(p)

    p = command("link", help="offline link of one checkout or worktree")
    p.add_argument("path", nargs="?")
    p.add_argument("--hook", action="store_true", help="session hook mode: read cwd from stdin, never fail")
    p.add_argument("--check", action="store_true", help="write nothing; exit 1 if links are missing")
    p.add_argument("--adopt", action="store_true", help="back up old copies and link them to main")

    p = command("unlink", help="remove the links acp made in one checkout")
    p.add_argument("path", nargs="?")
    p.add_argument("--dry-run", action="store_true")

    p = command("upgrade", help="update ak, pin the channel's latest kit, sync")
    p.add_argument("--channel", default="beta", choices=("stable", "beta"))
    p.add_argument("--version", help="pin this kit version instead of the latest")
    p.add_argument("--skip-binary", action="store_true")
    p.add_argument("--no-sync", action="store_true")
    p.add_argument("--dry-run", action="store_true", help="show the new pins; change nothing")
    no_fix(p)

    p = command("add", help="add a repo to the fleet")
    p.add_argument("path", nargs="?")
    p.add_argument("--id")
    p.add_argument("--kit", default="engineer")
    p.add_argument("--channel", default="beta")
    p.add_argument("--targets", default="claude-code,codex")
    p.add_argument("--no-agentkit", action="store_true", help="personal skills only")
    p.add_argument("--no-sync", action="store_true")
    p.add_argument("--dry-run", action="store_true")
    no_fix(p)

    p = command("remove", help="take a repo out of the fleet")
    p.add_argument("id")
    p.add_argument("--yes", action="store_true")

    p = command("activity", help="recent Claude/Codex sessions per repo")
    p.add_argument("--days", type=int, default=30)
    p.add_argument("--min", type=int, default=2, help="hide repos with fewer sessions")

    p = command("global", help="link global personal skills")
    p.add_argument("--check", action="store_true")
    p.add_argument("-v", "--verbose", action="store_true")

    p = command("hooks", help="session-start hooks that link worktrees")
    p.add_argument("action", nargs="?", default="status", choices=("status", "install", "uninstall"))
    p.add_argument("--dry-run", action="store_true")

    p = command("skills", help="show or change a project's skills")
    p.add_argument("action", choices=("list", "set"))
    p.add_argument("id")
    p.add_argument("--add", help="comma-separated skill names")
    p.add_argument("--remove", help="comma-separated skill names")
    p.add_argument("--runtimes", help="personal skills only: claude,codex (default: the project's targets)")
    p.add_argument("--apply", action="store_true", help="write the manifest and sync; default is a preview")
    no_fix(p)

    p = command("kit", help="give a project AgentKit")
    p.add_argument("action", choices=("add",))
    p.add_argument("id")
    p.add_argument("--kit", default="engineer")
    p.add_argument("--channel", default="beta")
    p.add_argument("--targets", default="claude-code,codex")
    p.add_argument("--apply", action="store_true", help="write the manifest and sync; default is a preview")
    no_fix(p)

    p = command("fix", help="coding-agent repair of a failed sync")
    fix = p.add_subparsers(dest="action", required=True)
    q = fix.add_parser("run", parents=[common], help="sync one project and hand a failure to the agent")
    q.add_argument("ids", nargs="*", help="project id (default: the one you are in)")
    q.add_argument("--runner", choices=("direct", "ak-run"))
    q.add_argument("--target", choices=tuple(TARGETS))
    q.add_argument("--attempts", type=int)
    q.add_argument("--dry-run", action="store_true", help="show the plan; no sync, backup or agent")
    q = fix.add_parser("list", parents=[common], help="fix sessions, newest first")
    q.add_argument("--web-op", help=argparse.SUPPRESS)
    q.add_argument("--limit", type=int, default=20)
    q = fix.add_parser("show", parents=[common], help="one fix session")
    q.add_argument("op")
    q.add_argument("--transcript", action="store_true")
    q = fix.add_parser("rollback", parents=[common], help="restore what a fix session backed up")
    q.add_argument("op")
    q.add_argument("--yes", action="store_true")
    q = fix.add_parser("config", parents=[common], help="show or set fix settings")
    q.add_argument("--runner", choices=("direct", "ak-run"))
    q.add_argument("--target", choices=tuple(TARGETS))
    q.add_argument("--attempts", type=int)
    q.add_argument("--timeout", type=int, help="seconds per attempt")
    q.add_argument("--auto", choices=("on", "off"), help="start a fix session when a sync fails")

    p = command("setup", help="check, install or sign in to the ak CLI")
    p.add_argument("action", nargs="?", default="check", choices=("check", "install-ak", "login"))
    p.add_argument("--channel", choices=("stable", "beta"), help="install-ak: default is the fleet's channel")
    p.add_argument("--version", help="install-ak: pin an ak CLI version (not the kit version)")
    p.add_argument("--apply", action="store_true", help="install-ak: download and run; default is a plan")

    p = command("describe", help="machine-readable contract of every command")
    p.add_argument("name", nargs="*")

    p = command("init", help="create a control plane from the template")
    p.add_argument("dir", type=Path)
    p.add_argument("--no-default", action="store_true", help="do not record it in ~/.config/acp/config.json")
    p.add_argument("--dry-run", action="store_true")

    command("mcp", help="MCP server on stdio")

    p = command("web", help="local UI on 127.0.0.1")
    p.add_argument("--port", type=int, default=8780)
    p.add_argument("--no-open", action="store_true")
    return parser


def kind_of(args) -> str:
    action = getattr(args, "action", None) if args.command in ("skills", "kit", "fix", "setup", "hooks") else None
    return "acp." + ".".join(filter(None, [args.command, action]))


def main() -> int:
    global TTY
    args = build_parser().parse_args()
    if not args.command:
        args = build_parser().parse_args(["status"] + (["--json"] if args.json else []))
    handlers = {
        "status": cmd_status, "doctor": cmd_status, "sync": cmd_sync, "link": cmd_link,
        "unlink": cmd_unlink, "upgrade": cmd_upgrade, "add": cmd_add, "remove": cmd_remove,
        "activity": cmd_activity, "global": cmd_global, "hooks": cmd_hooks, "skills": cmd_skills,
        "kit": cmd_kit, "fix": cmd_fix, "setup": cmd_setup, "describe": cmd_describe, "web": cmd_web,
        "init": cmd_init, "mcp": cmd_mcp,
    }
    if args.command not in ("init", "describe", "mcp"):
        root = find_root()
        if root is None:
            message = "acp: no control plane found; run `acp init <dir>` or set ACP_ROOT"
            if not args.json:
                raise SystemExit(message)
            print(json.dumps({"schema_version": 1, "kind": kind_of(args), "data": {}, "exit_code": 2,
                              "error": message.removeprefix("acp: ")}, indent=2))
            return 2
        use_root(root)
    if not args.json or args.command in ("web", "mcp"):
        return handlers[args.command](args)
    TTY = False
    out, error, code = io.StringIO(), None, 0
    real = sys.stdout
    try:
        with contextlib.redirect_stdout(out):
            code = handlers[args.command](args)
    except SystemExit as stop:
        code = stop.code if isinstance(stop.code, int) else 1
        error = None if isinstance(stop.code, int) else str(stop.code)
    envelope = {"schema_version": 1, "kind": kind_of(args),
                "data": {**({"logs": [log.to_dict() for log in EMITTED]} if EMITTED else {}), **RESULT,
                         "text": out.getvalue()},
                "exit_code": code}
    if error:
        envelope["error"] = error.removeprefix("acp: ")
    print(json.dumps(envelope, indent=2), file=real)
    return code


