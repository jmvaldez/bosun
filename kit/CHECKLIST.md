# Manual checks

Things only a human at the keyboard can confirm. The agent prepares the commands and marks the matching acceptance rows `BLOCKED` until you report back. Redact before saving any capture: keep key names and numbers, replace ids with placeholders, replace every text value with `<redacted>`.

Use a disposable test repo, not real work. All captures use per-run `--settings`, which never writes your settings file (FACTS F1).

## Before M1a: re-verify facts

- [ ] `claude --version` and `tmux -V` recorded in `build-log/FACTS-CHECK.md`.
- [ ] Each "Re-verify" command in FACTS.md run; differences noted.

## M1a captures

- [ ] Status line stdin: run `claude --settings '{"statusLine":{"type":"command","command":"tee /tmp/bosun-cap/sl.json"}}'`, send one prompt, wait for a tick, exit. Confirm `session_id`, `model`, `cost.total_cost_usd`. Save redacted to `testdata/statusline-stdin.json`.
- [ ] Subagent pair: capture `SubagentStart` and `SubagentStop` (FACTS F2 command). Run one foreground and one background subagent. Confirm both pairs share `agent_id`. Save to `testdata/hook-subagent-pair.jsonl`.
- [ ] Notification types: in an interactive capture session, trigger a permission prompt and wait 10 s; then leave a finished turn idle for 70 s. Confirm `permission_prompt` and `idle_prompt` payloads. Save to `testdata/hook-notification.jsonl`.

## M1b

- [ ] `/clear`: one prompt, `/clear`, one prompt, exit. Two session ids; the second total starts near $0. Save both transcripts redacted.
- [ ] Look at the real bar in your own terminal (attach to the test server): pills, tab markers, colors read correctly, nothing raw like `#{` or `bg=`.
- [ ] `prefix + a` walks waiting panes and wraps, with your real prefix key.
- [ ] Ctrl+C a working agent; the marker drops to idle within 6 s.

## M2: spend against Claude Code

- [ ] Spend a little across a few prompts in three sessions. In each, run `/cost` before exiting. After exit, `bosun spend --by session` matches each to the cent.
- [ ] One session using a subagent: total still matches `/cost`; `unattributed` is positive.
- [ ] `/branch` a session, spend a little in both, exit both. Parent counted once; fork shows only its own spend.
- [ ] `/login` mid-session (if you can), then spend more. Total keeps the pre-login spend.
- [ ] If your provider has a usage page: compare bosun's month-to-date with its month-to-date, not today's bar (daily bars can lag hours and may use the UTC day). Try `spend.day = "utc"`.

## M3: notifications (macOS)

- [ ] `brew install terminal-notifier`, run `terminal-notifier -message test`, allow notifications.
- [ ] `bosun notify --test` shows a banner.
- [ ] Needs-you with your terminal in the background: banner with sound within about 1 s.
- [ ] Needs-you with the waiting pane focused and the terminal frontmost: no banner.
- [ ] Answer the prompt: the banner disappears from Notification Center.
- [ ] A turn over 20 s while away: silent done banner. Under 20 s: none.
- [ ] Click a banner: your terminal comes forward and the pane switches. A bad session id fails quietly. Save notes to `testdata/notifier-click-notes.txt`.
- [ ] Set `rules.session_cost = 0.01`: the banner shows a real dollar figure. Reset after.
- [ ] Detach, let a runaway fire, reattach: it shows in the segment with no banner.

## M4 and M5

- [ ] `prefix + W` opens the popup; keys work; `enter` jumps; glyph columns line up in your font.
- [ ] Run a session with a subagent: its `└` row shows under the parent with a ticking runtime and tool count, then drops into SUBS when it ends.
- [ ] Spend tab: expand a task down to request rows with `l`/`→`, collapse with `h`/`←`; the box-drawing and bar glyphs render in your font.
- [ ] `s` on an agent row opens its spend tree; `d` opens a diff window at that session's cwd.
- [ ] Shrink the terminal below 110 columns: the summary pane collapses above the tree.
- [ ] Peek: trigger a permission prompt for a Bash command; `v` on that row shows the command. Then grep the bosun state, cache, and data dirs for it: no hits.
- [ ] Confirm `Escape` in a Claude Code pane interrupts a running turn on your version (else set `ui.interrupt_keys`). Then `!` on a working row, `y`: the turn stops and the row goes idle within 6 s. `n` does nothing.
- [ ] `x` on a runaway row: the pill, tab marker, and banner disappear; let the condition clear and re-trip, and it fires again.
- [ ] Spend tab `e`: the CSV opens in your spreadsheet app and its total matches the tab.
- [ ] `bosun run --task ABC-123 --worktree feat/ABC-123` in a test repo: worktree created beside it, window opens there, task shows `ABC-123`; rerun refuses the existing path.
- [ ] `bosun run --task ABC-123` from a pane: named window, your `<launcher>` ran, hooks fired (session shows in the popup), window closes on exit.

## M6

- [ ] `bosun install`, follow its printed steps by hand, then `bosun doctor` passes.
- [ ] Your settings file and tmux conf are unchanged by `install` and `uninstall` (checksum before and after; if they live in a git repo, `git status` is clean).

## Pace rule

- [ ] After 7 or more days with spend, `bosun spend --by day` and the popup footer show a pace ratio that matches your sense of a normal day.
- [ ] Temporarily lower `pace.min_over` to `0.01` and `pace.alert` to `1.0`: one pace banner, not two. Reset after.

## Linux (desktop or VM)

- [ ] `bosun ui` raw mode works in a tmux pane.
- [ ] `bosun notify --test` shows a `notify-send` toast; `notify-send --print-id` works on the distro.
- [ ] Retract: needs-you toast disappears when answered.
- [ ] With `focus-events on` and a focus-reporting terminal, the focused pane suppresses needs-you; without the `focus` termfeature, bosun always notifies.

## WSL

- [ ] `wsl-notify-send.exe` toast from `bosun notify --test` inside tmux.
- [ ] Without it on PATH, the PowerShell toast appears.
- [ ] Windows Terminal alt-tab and tab switch clear tmux `focused` (if not, set `focus.wsl = "never"`).
- [ ] Restart the WSL instance while an old tmux server runs; the next toast retries with the newest `WSL_INTEROP` and appears.
- [ ] `bosun doctor` fails when the state dir sits on `/mnt/c`.
- [ ] Save notes to `testdata/wsl-notes.txt`.

## Final

- [ ] Run the demo script end to end. `readlink ~/.local/bin/claude` is the same before and after.
- [ ] Nothing under your real bosun dirs changed during the demo.
