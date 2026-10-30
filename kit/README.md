# bosun rebuild kit

Everything a fresh Claude Code session needs to rebuild bosun from scratch: a tmux-native watcher for Claude Code agent sessions (attention, runaways, spend). The spec is locked, the Claude Code facts are captured with re-verify commands, and every bug from the first build is a rule or an acceptance check.

## Files, in the order to use them

| File | Use |
|---|---|
| README.md | this page |
| PROMPT.md | fill in the table, then paste as the kickoff |
| SPEC.md | what to build: architecture, data model, rules, tmux surface, milestones, acceptance |
| FACTS.md | verified Claude Code behavior, with versions and re-verify commands |
| LESSONS.md | every bug from the first build: symptom, cause, rule, test |
| CONTEXT.md | glossary |
| CHECKLIST.md | the checks only you can do |

## Steps

1. Create an empty repo dir and copy this kit into `<repo>/kit/`.
2. Fill in the "Fill in before starting" table in `PROMPT.md`.
3. Start Claude Code in the repo dir and paste the kickoff below, or the whole of `PROMPT.md`.
4. Work through `CHECKLIST.md` as each milestone marks rows `BLOCKED`.

## Kickoff (one line)

```
Read kit/PROMPT.md and follow it exactly; my fill-ins are in its table.
```

## Before you start

- macOS with Go 1.26+, tmux 3.3+ (popups and `client_termfeatures`), and Claude Code installed.
- `terminal-notifier` for desktop notices (`brew install terminal-notifier`), optional.
- Know how you launch Claude Code (`<launcher>`): plain `claude`, an alias, or a wrapper function.
- If your shell rc rewrites PATH, expect the session to re-export it inside `bosun run` commands.
