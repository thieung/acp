# Personal skills

Put each skill in its own folder with a `SKILL.md`, for example `skills/release-notes/SKILL.md`.
A project gets a skill when its manifest lists it:

```json
"skills": [{ "name": "release-notes", "runtimes": ["claude", "codex"] }]
```

`acp sync <project>` links the folder into `.claude/skills/` and `.agents/skills/` of that
project and of every worktree. `global-skills.json` links skills into your home folders
instead (`acp global`).
