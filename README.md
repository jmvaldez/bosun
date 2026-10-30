# bosun

bosun shows the state and cost of every Claude Code session running in tmux.

```
⚑ 2 waiting  ▲ 1 runaway  ✓ 1 done  $23.40 / $50
```

- The status bar counts sessions stopped at a permission prompt or a finished
  turn, usually within a second. `prefix + a` jumps to the oldest one.
- `prefix + W` opens a popup with every session's task, repo, spend, and what
  it's doing right now (`running go test ./...`, `editing ui/view.go`,
  `thinking`, `waiting on 2 subagents`), with how long the turn has run.
- Alerts fire for long-running or expensive subagents, sessions crossing a cost
  step, quiet sessions, and days spending above your usual pace.
- `bosun spend` breaks cost down by day, task, session, prompt and request, with
  subagents rolled into their parent. Totals match Claude Code's to the cent.
- Desktop notices fire when a session needs you, finishes, or fails. The pane
  you're looking at is skipped.

It runs locally with no daemon or telemetry, and bosun itself makes no network calls. It does shell out to `claude auth status --json` and `claude agents --json` (every 5s while the popup is open), and the claude CLI may contact Anthropic.

Version 0.1.0, pre-release. Tested on Linux (Pop!_OS 22.04, tmux 3.2a, Claude
Code 2.1.309+). macOS notifications are untested. WSL is not supported yet.

## Install

Requires tmux 3.2+ and Claude Code with plugin support.

```sh
brew install jmvaldez/tap/bosun
bosun setup
```

`bosun setup` asks one question, whether to show live session cost, then:

1. installs the Claude Code plugin (`bosun@bosun`)
2. if you said yes, wraps `statusLine.command` in `~/.claude/settings.json`
   (after backing it up); if you said no, makes sure it is not wrapped
3. generates bosun's own tmux config and loads it into the running tmux. It never
   edits your tmux config: the plugin's SessionStart hook loads it again whenever
   a Claude Code session starts in a tmux server that has not, such as after a
   restart or an upgrade
4. runs the doctor checks and shows only failures

`bosun setup --yes` and `--no` answer for you. With neither flag and no terminal
it asks nothing, prints ``run `bosun setup` in a terminal`` and exits 2 (0 if
bosun is already set up). Run `bosun setup` again any time to change the answer;
running bare `bosun` in a terminal before setup starts it too. Restart Claude
Code and send one prompt so the doctor's hook checks pass.

### From source

Needs Go 1.26+.

```sh
git clone https://github.com/jmvaldez/bosun && cd bosun
make install            # ~/.local/bin/bosun; set PREFIX to change
bosun setup
```

To make the edits by hand instead, run `bosun install` without flags and it
prints them.

Don't register bosun hooks in `settings.json` yourself. They would fire twice.

Desktop notices need `notify-send` or `gdbus` on Linux, `terminal-notifier` on
macOS.

The `.deb` and `.rpm` packages install the binary and plugin; then run
`bosun setup`.

## Usage

### Keys

| Key | Action |
|---|---|
| `prefix + W` | session popup |
| `prefix + $` | popup, spend view (replaces rename-session) |
| `prefix + a` | next waiting session: needs-you first, then done. Repeat to walk the queue |

Popup: `enter` jump, `a` oldest waiting, `v` pending tool call, `x` dismiss
alert, `!` interrupt (confirms first), `d` diff, `s` spend for row, `tab` switch
view, `q` close. Spend view: `h`/`l` fold, `g` group, `p` range, `e` export CSV.

The spend figure in the bar turns amber, then red, when today runs above your
usual pace.

### CLI

```
bosun status [--tmux | --json]          current state of every session
bosun spend [--by K] [--since D] [--task T] [--json | --csv]
                                        K: day, task, repo, session, prompt, request, model, subagent
bosun jump [--queue]                    switch to the next waiting pane
bosun ui                                open the popup
bosun tag TEXT | --clear                set or clear the current session's task
bosun run --task TEXT [--dir P] [--worktree BRANCH] [-- CLAUDE_ARGS]
                                        open a Claude Code window with a task set
bosun ack ID | --all                    dismiss alerts until their conditions clear
bosun backfill [--since 30d]            import transcript history
bosun doctor [--json]                   self-check; non-zero on failure
bosun notify --test                     send a test notice
bosun config                            print the effective config
bosun uninstall [--apply] [--purge]     reverse the install
```

### Tasks

A task labels a session for the popup and for `bosun spend --by task`. It can be
an issue key or any short text:

```sh
bosun tag ABC-123
bosun tag "auth refactor"
```

bosun uses the first of:

1. `bosun tag TEXT`
2. `BOSUN_TASK`, set by `bosun run --task`
3. an issue key in the branch the session started on
4. the transcript's `gitBranch` (backfill only)

With none, the session counts as `untasked`, and the popup shows Claude Code's
session name in grey (unless `privacy.show_text` is `false`).

Tags are trimmed, internal spaces collapse, and case is kept. Max 64 bytes, no
control characters. An exact issue key is uppercased so `abc-123` and `ABC-123`
group together. `bosun spend --task` matches case-insensitively.
`task.key_regex` and `task.prefixes` only apply to branch and transcript keys.

## Configuration

`~/.config/bosun/config.toml`. Every key is optional and the file is re-read
each second. `bosun config` prints effective values. Invalid values fall back to
the default and `bosun doctor` reports them.

| Key | Default | Meaning |
|---|---|---|
| `rules.subagent_runtime` | `"10m"` | subagent wall-time alert |
| `rules.quiet` | `"5m"` | gap between tool calls before a working session counts as quiet |
| `rules.subagent_cost` | `2.00` | subagent cost alert, USD (estimated) |
| `rules.session_cost` | `10.00` | alert at each multiple of this, USD |
| `rules.daily_budget` | `0` | daily cap, USD; `0` turns it off |
| `pace.enabled` | `true` | alert when today runs well above typical |
| `pace.amber` / `pace.alert` | `1.25` / `1.5` | ratio to typical for amber and for an alert |
| `pace.min_over` | `20.00` | an alert also needs this many USD over typical |
| `pace.min_days` / `pace.window_days` | `7` / `30` | baseline days needed, and lookback |
| `spend.day` | `"local"` | day boundaries: `local` or `utc` |
| `task.key_regex` | `"[A-Z][A-Z0-9]+-\\d+"` | issue key pattern |
| `task.prefixes` | `[]` | allowed key prefixes; empty accepts any |
| `tmux.popup_key` / `jump_key` / `spend_key` | `"W"` / `"a"` / `"$"` | bindings after the prefix |
| `claude.bin` | `""` | path to `claude`; empty searches PATH |
| `claude.launcher` | `"claude"` | what `bosun run` types in your shell (an alias or wrapper works) |
| `notify.backend` | `"auto"` | `auto`, `notify-send`, `terminal-notifier`, `osascript`, `wsl`, `off` |
| `notify.done_min_turn` | `"20s"` | shortest turn that gets a "finished" notice |
| `notify.max_per_minute` | `6` | rate limit |
| `ui.diff_cmd` | `"git diff"` | what popup `d` runs |
| `ui.interrupt_keys` | `"Escape"` | keys popup `!` sends |
| `ui.glyphs` | `"unicode"` | `ascii` swaps ⚑ ▲ ✓ for ! ^ + |
| `bar.style` | `"labeled"` | `compact` for the terse bar |
| `privacy.show_text` | `true` | show pending commands and prompt first lines (read live, never stored) |
| `run.worktree_dir` | `""` | parent dir for `run --worktree` |

Directories follow `XDG_*`, or `~/.config`, `~/.local/state`, `~/.cache` and
`~/.local/share`. Override with `BOSUN_CONFIG_DIR`, `BOSUN_STATE_DIR`,
`BOSUN_CACHE_DIR` or `BOSUN_DATA_DIR`.

## Privacy

Prompt text is never stored or logged. Hook decoders keep an allowlist of
fields.

With `privacy.show_text = true` the popup and notices show the pending command,
a question, or a prompt's first line. These are read from the transcript at
render time and not written to disk. The grey session name comes from
`claude agents --json`, polled every 5 s and held in memory. Set
`show_text = false` to hide all of it.

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

## Known limits

- `bosun` must be on the PATH Claude Code hooks inherit. Hooks fail silently, so
  a broken install shows an empty bar. Run `bosun doctor`.
- Only sessions in tmux panes are tracked. After a tmux restart or resurrect
  restore they show `? pane gone` until their next hook.
- Escape at a permission prompt fires no hook. needs-you clears 6–10 s later via
  `claude agents --json`.
- Linux notice clicks work for 2 minutes after posting.
- macOS click-through and focus detection aren't built.

## Uninstall

```sh
bosun uninstall --apply           # reverses each install step, with backups
bosun uninstall --apply --purge   # also deletes state, cache and data dirs
```

`--purge-config` also deletes the config. Purge refuses symlinks and any dir
without bosun's marker file.

## Development

```sh
make build        # bin/bosun (static, CGO_ENABLED=0)
make test         # go test ./...
make test-short   # skips latency benchmarks
make check        # vet, test, cross-compile, staticcheck
make release      # GoReleaser snapshot into dist/
scripts/demo.sh up && scripts/demo.sh attach   # private tmux server with throwaway state
```

`BOSUN_TEST_TMUX=1` runs tests against a private tmux server.
`BOSUN_TEST_REAL_SESSIONS=1` reads your real transcripts, read-only.

`CONTEXT.md` defines terms, `kit/` holds the spec, `build-log/` holds decisions
and milestone notes.

## License

MIT. See [LICENSE](LICENSE).
