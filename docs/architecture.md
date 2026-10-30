# Architecture

## How it works

```
Claude Code ──hooks──▶ bosun hook ──▶ events.jsonl ─┐
            ──status line──▶ bosun statusline ──────┤
            ──transcripts──────────────────────────┤
                                                    ▼
tmux status-interval (1 s) ──▶ bosun status --tmux ──▶ segment, tab markers, alerts, notices
                                                    │
                                     bosun.db (SQLite ledger) ◀── ingest
```

- The plugin registers 13 hook events. `bosun hook` appends one line to a state
  file and exits 0 in a few milliseconds.
- `bosun statusline -- <your command>` records the session total, then runs your
  original status line.
- tmux runs `bosun status --tmux` each second to evaluate state and alert rules,
  update window markers and print the segment.
- Transcripts are ingested into SQLite for the per-prompt and per-request
  breakdown.

## Spend accuracy

All figures are estimates at API list prices, regardless of plan.

| Session | Total comes from |
|---|---|
| running | the wrapped status line |
| ended | Claude Code's `cost-state` |
| neither (pre-install, crashed, `claude -p`) | transcript pricing, marked `est.` |

That table is live cost **on**, which is when `statusLine.command` in
`~/.claude/settings.json` is bosun's wrapper (bosun reads the file; it keeps no
flag). With live cost **off**, a session with neither source shows `—` instead of
an `est.` figure, in the tmux bar, the popup and `bosun spend`, and totals leave
it out with a note such as `+ 2 —`. Ended sessions with an exact `cost-state` figure
show as usual. Cost alerts (session cost, daily budget, pace) then judge exact
figures only; the subagent cost rule stays an estimate, labelled `est.`.

Any gap between the total and the per-request breakdown shows as `unattributed`.
Forked sessions don't double-count their parent.

## Privacy

Prompt text is never stored or logged. Hook decoders keep an allowlist of
fields.

With `privacy.show_text = true` the popup and notices show the pending command,
a question, or a prompt's first line. These are read from the transcript at
render time and not written to disk. The grey session name comes from
`claude agents --json`, polled every 5 s and held in memory. Set
`show_text = false` to hide all of it.

## Code layout

`main` is the composition root: `main.go` builds the platform adapters once,
each command lives in its own `cmd_<name>.go`, and `commands.go` holds the
command table and dispatch. Everything else is under `internal/`, grouped here
by role. `CONTEXT.md` defines the terms.

Hot path (runs on every hook):

- `hook` records one Claude Code hook event; the hottest path in bosun.
- `statusline` records the live session total, then runs your real status line.
- `stdin` reads a pipe without ever blocking past a deadline.
- `heartbeat` records when hooks last ran and how long they took.

Evaluation:

- `eval` folds hook events into `live.json`, runs the alert rules and answers the segment, `status` and `jump`.
- `live` holds per-session state: the event fold and the `live.json` file.
- `rules` keeps alert state, at most one alert per rule and scope.
- `subcost` estimates what a running subagent has spent from its transcript.
- `pace` compares today's spend with the median of earlier days.

Spend:

- `ledger` is the SQLite store; only CLI processes open it.
- `spend` turns ledger rows into the day, task, repo, session, prompt, request, model and subagent views.
- `prices` is the only place tokens become dollars.
- `refresh` is the detached job that keeps the agents cache, account key, ledger and today's spend current.

Surfaces:

- `ui` is the popup, a pure model behind small terminal and tmux interfaces.
- `segment` renders the tmux status segment and the text summary.
- `notify` renders and posts desktop notifications.
- `jump` picks the next pane in the waiting queue.
- `peek` reads one line of text from a session's transcript.

Setup and checks:

- `install` owns first-run setup and the edits it makes.
- `uninstall` prints the steps to remove bosun and, with `--purge`, deletes its dirs.
- `doctor` is the read-only self-check.
- `tmuxconf` generates the tmux conf bosun owns.
- `plugstate` reads Claude Code's plugin bookkeeping, read-only.

Claude Code and tmux:

- `claude` reads what bosun learns from the `claude` CLI.
- `tmux` wraps the few tmux commands bosun runs.
- `worktree` adds the git worktree `bosun run --worktree` asks for.
- `gitinfo` reads repo and branch straight from `.git` files.
- `task` is the one issue-key parser.

Plumbing:

- `paths` resolves bosun's directories.
- `platform` holds the OS seams and the adapters bundle `main` hands down.
- `runner` runs child processes, each with a deadline.
- `flock` takes advisory locks that never wait forever.
- `atomicfile` writes state files as temp file plus rename.
- `clean` turns decoded strings into values safe to store.
- `config` loads `config.toml`.
- `term` puts the terminal in raw mode and decodes keys.
- `shell` quotes strings for a POSIX shell.
- `testguard` keeps tests out of the real home.
