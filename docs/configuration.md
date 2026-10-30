# Configuration

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

## Tasks

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

## What `bosun setup` does

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
