# Coding-agent skill transcripts

Context: a developer machine where coding agents (Claude Code, Codex) work
on projects that deploy to Skali. The skill content ships inside the CLI
binary; install is user-level, and the installed directory is owned by the
command: reinstalling refreshes the content and overwrites local edits.

## 1. Interactive install (every agent preselected)

```console
$ skali skill install
◆  Install the skali skill for which agents?
└  Claude Code, Codex
✓ installed Claude Code skill to ~/.claude/skills/skali (4 files)
✓ installed Codex skill to ~/.agents/skills/skali (4 files)
Restart agent sessions to pick up the new skill.
```

## 2. Non-interactive install

```console
$ skali skill install --agent claude
installed Claude Code skill to ~/.claude/skills/skali (4 files)
Restart agent sessions to pick up the new skill.

$ skali skill install --all
installed Claude Code skill to ~/.claude/skills/skali (4 files)
installed Codex skill to ~/.agents/skills/skali (4 files)
Restart agent sessions to pick up the new skill.
```

## 3. Non-interactive without a selection

```console
$ skali skill install < /dev/null
error: non-interactive runs must select agents: pass --agent claude, --agent codex, or --all
```

## 4. Refusing a skill directory skali does not own

A `skali` skill directory whose SKILL.md lacks the managed marker was made
by the user; install refuses instead of replacing it.

```console
$ skali skill install --agent claude
error: install Claude Code skill: /home/dev/.claude/skills/skali exists but was not installed by skali; remove the directory to let install replace it
```

Binding here: the user-level scope, the overwrite-and-prune ownership of
the installed directory, the refusal to replace an unmanaged directory,
and the explicit selection requirement in non-interactive runs.
