# HANDOFF

Everything below was exercised on this machine (Pop!_OS 22.04, tmux 3.2a, Claude Code 2.1.309 and 2.1.310). Nothing here edits your settings, your tmux config, or your shell rc files; the install steps in part B are the ones you run by hand.

## A. Drive the demo

The demo runs a private tmux server (`-L bosun-demo`, no config file), temporary `BOSUN_*_DIR`s under `/tmp/bosun-demo-<you>`, the plugin through `claude --plugin-dir`, and a wrapped status line through `claude --settings`. It never sets `XDG_*` (it unsets any that are set), never contacts your tmux server, and never edits `~/.claude/settings.json`. Your real Claude transcripts are read, read-only, so the Spend tab shows your real history.

```sh
cd /mnt/shared/dev/bosun
scripts/demo.sh up          # rebuilds bin/bosun, writes the generated tmux conf, starts the server
scripts/demo.sh attach      # nested tmux is fine; the prefix inside the demo is C-b
scripts/demo.sh agent ABC-1 # a Claude Code window tagged ABC-1 (BOSUN_TASK); extra claude args after --
scripts/demo.sh down        # stops it, then checks the claude symlink, settings.json and real bosun dirs
scripts/demo.sh reset       # down, then deletes the demo dir (it refuses to delete anything else)
```

Other commands: `scripts/demo.sh bosun status|spend|ack|notify --test ...` runs the built binary against the demo's state; `scripts/demo.sh env` prints the exports if you want a shell of your own.

Knobs, set before `up`: `DEMO_NOTIFY=off` for no desktop notices (default `auto`: gdbus on this machine), `DEMO_SESSION_COST=0.05` to lower the runaway step, `DEMO_THEMEPACK=0` to skip your tmux-themepack theme. The theme is sourced from `~/.tmux/plugins/tmux-themepack/powerline/block/blue.tmuxtheme` (read only) before the bosun conf, as in a real install.

The first `agent` in the project dir shows Claude Code's own trust dialog (choose "Yes, I trust this folder"); Claude records that in `~/.claude.json`, which is Claude's file, not bosun's. The demo project is `/tmp/bosun-demo-<you>/project`, a throwaway git repo. Windows use `--model haiku --permission-mode default`, so prompts stay cheap and permission prompts happen.

### What to try

1. **Needs-you.** In an `agent` window ask: `Run the shell command: mkdir zzdemo`. The prompt appears. Switch to the `shell` window (`C-b 0`): the bar shows ` ! 1 ` and the tab shows `!` in front of `1:ABC-1`, and a desktop notice appears ("waiting on you: permission prompt"). A notice is only posted when you are not looking at that pane, so stay on another window. Answer, or press Escape: the notice is withdrawn (Escape fires no Claude hook, so bosun notices on the second `agents --json` read, about 6 s).
2. **Done.** `Reply with one word: hi. No tools.` Turns shorter than 20 s end silent (`notify.done_min_turn`); a longer turn gives a quiet "turn finished" notice if you are elsewhere. `✓1` shows in the bar until you look at the pane.
3. **Jump.** With two or more waiting agents, `C-b a` walks needs-you (oldest first) then done, and wraps; with nothing waiting it says so on the status line.
4. **Popup.** `C-b W`. Agents tab: `j/k` move, `enter` jumps, `a` jumps to the oldest waiting, `tab` switches. Spend tab: `l`/`h` fold, `p` today/7d/30d, `g` task/repo/session/model, `e` writes a CSV under the demo's `data/exports/` and shows the path.
5. **Peek.** On a needs-you row press `v`: a second line shows the pending tool call (`Bash: mkdir zzdemo`). It is read from the transcript for that frame and never stored. In the Spend tab, `v` on a `prompt #N` row shows that prompt's first line.
6. **Ack.** `x` in the popup, or `scripts/demo.sh bosun ack --all`, hides runaway alerts until their condition clears; it fires again if it clears and re-trips.
7. **A subagent.** `Use the Agent tool with an Explore subagent to list the files in this directory.` The popup shows an indented `└ ● Explore` row with a ticking runtime and tool count, then it moves into SUBS (`0▶ 1`). On Claude Code 2.1.309 subagents always run in the background.
8. **A runaway.** Set `DEMO_SESSION_COST=0.01` before `up` (or edit `/tmp/bosun-demo-<you>/config/config.toml`, which is re-read every second, and put `session_cost = 0.001` under `[rules]`). After the first reply the bar shows ` $! 1 `, the tab shows `$!`, and the row reads `runaway` with `session $0.00x`. `ack` clears it. `rules.subagent_runtime` and `rules.quiet` work the same way; shorten them to see those.
9. **Interrupt.** In the popup on a working row press `!`, then `y`: bosun re-checks the pane and sends Escape. `n` sends nothing.
10. **Tag.** In the agent window's pane: `scripts/demo.sh bosun tag zz-9 --session <id>` (ids are in `scripts/demo.sh bosun status --json`; with no `--session` and `$TMUX_PANE` set, `bosun tag KEY` targets the newest live session in that pane). The row's TASK changes everywhere at once; `tag --clear` puts the branch or env key back.
11. **Run.** `scripts/demo.sh bosun run --task ABC-123 --dir /tmp/bosun-demo-<you>/project -- --plugin-dir /mnt/shared/dev/bosun/plugin --settings /tmp/bosun-demo-<you>/settings.json --model haiku --permission-mode default` opens a window named `ABC-123` through your login shell (`$SHELL -ic`, so your `~/.zshrc` and any `claude` alias apply, with PATH re-exported after it), and it closes when Claude exits. `--worktree feat/ABC-123` makes `<repo>-feat-ABC-123` beside the repo first (it never reuses or removes a path).
12. **Doctor.** `scripts/demo.sh bosun doctor` (read-only; add `--json`). Against the demo it reports two FAILs by design: the plugin and wrapped status line are loaded per run, not installed in your real settings.
13. **Click (Linux).** With another window selected, when the banner "waiting on you" appears, click it: the demo's client should switch to the agent's window. This is the one M3 live check that needs your hand; see `M3.md`.

What the last demo run looked like (this machine; one agent window, a low `session_cost`, the popup open over the shell window). The bar (the status line of the outer pane):

```
demo  jmvaldez  0:0                                                        0:shell* ✓ 1:ABC-1-                                                       ✓1 $17.85 est.  1
```

and the popup rows (whitespace squeezed):

```
[Agents]  Spend   1 agents · 1 repos  $! 1   today $17.85
STATE   FOR   WHERE   TASK   DETAIL   SUBS   SPEND
> $! runaway   16s   demo:1.0 project ABC-1   session $0.00   -   $0.00
```

Within that one up/down cycle `down` reported: `~/.local/bin/claude` unchanged, no real bosun dir created, `~/.claude/settings.json` unchanged. (Claude Code updates itself in place between cycles, so the check only compares within one.)

## B. Install for real (you run these)

Primary path (D-31):

```sh
cd /mnt/shared/dev/bosun && make install && bosun setup
```

`bosun setup` asks one question, "Show live session cost?" (`--yes` / `--no` answer it; without a terminal and without a flag it changes nothing). It writes bosun's own files, installs the plugin (skipped if already enabled), wraps `statusLine.command` in `~/.claude/settings.json` on yes (all other keys, key order and indent are kept; a `settings.json.bak-bosun-<timestamp>` copy is made first; invalid JSON refuses that step), and loads bosun's tmux conf into the running tmux without editing `~/.tmux.conf`. It ends with the `bosun doctor` checks. A heartbeat or status line FAIL clears after you restart Claude Code and send one prompt. `bosun uninstall --apply` reverses each step (plan, one confirmation, backups), also removes the `source-file` line an older `install --apply` added to the tmux conf, unbinds bosun's keys and hook in a reachable tmux server, and runs `--purge` only after the yes. `install --apply` was removed (D-32).

### B1. The same steps by hand (alternative)

`bosun install` with no flags prints each edit and writes only bosun's own files.

1. Build and install the binary (a temp file in `~/.local/bin`, then `mv`):
   ```sh
   cd /mnt/shared/dev/bosun && make install
   command -v bosun        # should print ~/.local/bin/bosun
   ```
2. The plugin, from this repo as its own marketplace:
   ```sh
   claude plugin marketplace add /mnt/shared/dev/bosun
   claude plugin install bosun@bosun
   ```
   Do not also add bosun hooks to settings.json; they would fire twice.
3. Write bosun's own files and print the edits for your setup (it does not touch your settings or tmux config):
   ```sh
   bosun install
   ```
   It writes `~/.config/bosun/config.toml` (all keys commented), `~/.local/state/bosun/identity.json`, and `~/.local/share/bosun/tmux.conf`.
4. Status line. In `~/.claude/settings.json` replace `statusLine.command` with this string (`bosun install` prints the same, built from your current command, your jq one-liner, wrapped so that if `bosun` ever goes missing the original runs unchanged):
   ```
"command": "if command -v bosun >/dev/null; then exec bosun statusline -- sh -c 'input=$(cat); model=$(echo \"$input\" | jq -r \".model.display_name // \\\"claude\\\"\"); dir=$(echo \"$input\" | jq -r \".workspace.current_dir // empty\" | xargs -r basename); used=$(echo \"$input\" | jq -r \".context_window.used_percentage // empty\"); out=\"$model\"; [ -n \"$dir\" ] && out=\"$out | $dir\"; [ -n \"$used\" ] && out=\"$out | ctx $(printf \"%.0f\" \"$used\")%\"; printf \"%s\" \"$out\"'; else exec sh -c 'input=$(cat); model=$(echo \"$input\" | jq -r \".model.display_name // \\\"claude\\\"\"); dir=$(echo \"$input\" | jq -r \".workspace.current_dir // empty\" | xargs -r basename); used=$(echo \"$input\" | jq -r \".context_window.used_percentage // empty\"); out=\"$model\"; [ -n \"$dir\" ] && out=\"$out | $dir\"; [ -n \"$used\" ] && out=\"$out | ctx $(printf \"%.0f\" \"$used\")%\"; printf \"%s\" \"$out\"'; fi"
   ```
   Keep `"type": "command"` and any other statusLine keys as they are.
5. tmux. In `~/.tmux.conf`, after the tpm line (`run '~/.tmux/plugins/tpm/tpm'` is line 91 today), add:
   ```
   source-file -q /home/jmvaldez/.local/share/bosun/tmux.conf
   ```
   It must come after tpm so themepack has built `status-right` and the window formats first. Then re-source once (`C-a r` runs `source-file ~/.tmux.conf`). Sourcing twice is harmless. Themepack's powerline themes already set `status-interval 1`. `C-a W` opens the popup and `C-a a` walks the waiting agents; both keys are free in your config.
6. Check: start `claude` in a tmux pane and ask it something; the bar should show your agent, and `bosun status` and `bosun spend --by day` should print. `bosun notify --test` posts one desktop notice. Then `bosun doctor` (read-only) should pass; any FAIL says what to fix.

To undo: `bosun uninstall` prints the steps in order (plugin uninstall, the exact original `statusLine.command` recovered from the wrapper, the tmux line to delete, the binary to remove). `bosun uninstall --purge` additionally deletes bosun's state, cache and data dirs (it refuses symlinks, anything that is not exactly the resolved bosun dir, and any dir without bosun's `.bosun` marker file; bosun writes the marker into its dirs on normal use, and dirs from older builds get it on the next command other than `doctor`, `config`, `uninstall`, `version`. If you pointed a `BOSUN_*_DIR` at a dir holding other files it is never marked; `touch` the marker yourself to purge it); `--purge-config` deletes the config too.

### B2. Install from a package instead (after the owner steps in `RELEASE.md`)

`.deb`/`.rpm` put `/usr/bin/bosun` and the plugin under `/usr/share/bosun` and print `bosun setup` (the package itself never edits your files). The plugin directory is found automatically. `claude plugin marketplace add /usr/share/bosun` replaces the repo path in step 2. Homebrew: `brew install jmvaldez/tap/bosun`. See `PACKAGING.md`.

## C. Open items

Needs you at the keyboard:

- **M2 spend vs `/cost`.** In three real sessions run `/cost` before exiting, then `bosun spend --by session --since today` and compare to the cent. One session with a subagent (total matches, `unattributed` positive). `/branch` a session and spend in both (parent counted once). `/login` mid-session (needs the wrapped status line). Compare month-to-date against your provider's usage page, with `spend.day = "utc"` too.
- **M3 live checks that need a focus-reporting terminal:** needs-you with the pane focused gives no notice; a turn over 20 s while away gives a silent done notice, under 20 s none; set `rules.session_cost = 0.01`, see a real dollar figure, reset it; detach, let a runaway fire, reattach: it shows in the bar with no banner. (Click-through is macOS only and not built.)
- **M4:** the popup glyphs (`└ ● ✓ ▸ ▾ ⤷ █ ░`) line up in your font; `Escape` interrupts a running turn in your Claude Code version (else set `ui.interrupt_keys`); the Spend tab's box-drawing renders.
- **Pace rule:** after 7 or more days with spend, `bosun spend --by day` and the popup footer show a pace ratio that matches your sense of a normal day; with `pace.min_over = 0.01` and `pace.alert = 1.0` you get one pace notice, not two. Reset after.
- **Final:** run the demo end to end once with your own eyes; nothing under `~/.local/state/bosun`, `~/.local/share/bosun`, `~/.config/bosun`, `~/.cache/bosun` changes during it.
- **M3 Linux click:** a physical click on a banner switches the pane (needs your hand; `M3.md` has the steps).
- **Release:** nothing has been pushed, tagged or signed. `RELEASE.md` lists the exact owner steps (license, first commit, repo, signing key and secret, tap repo and token, Pages, tag).
- Not built, so not testable yet: WSL (M8); macOS live checks (D-1).

## D. Known limits

- **`bosun` must be on the PATH Claude Code hooks inherit.** `make install` puts it in `~/.local/bin`, which they have. Hooks exit 0 silently if it is missing or fails, so a broken install shows as an empty bar, not an error. `doctor` (M6) will say why.
- **Only agents in tmux panes count.** A Claude Code session outside tmux has no pane to mark or jump to. After a tmux restart or tmux-resurrect restore, pane ids change, so restored agents show `? pane gone` in the popup until their next hook rebinds them (D-5).
- **Escape at a permission prompt fires no hook.** needs-you ends when `agents --json` has said "not waiting" twice, about 6 to 10 s later (D-7).
- **Two tmux servers, one state dir.** Markers are kept per server (marks.json); an evaluation from one server leaves the other server's sessions, markers and alerts alone, and does not count them. The popup's refresh is a normal evaluation (it can post and retract notifications; focus suppression still applies).
- **Subagents are background by default** on 2.1.309: a turn ends (`Stop`) while a subagent is still running, and bosun keeps the session `working` until its follow-up turn (D-8). Subagent cost in the popup and the `subagent_cost` rule is a transcript estimate, always marked `est.`.
- **Spend is an API-rate estimate** on any plan. Totals come from the status line (running sessions) and Claude's own `cost-state` (ended ones), so they match Claude to the cent; sessions with neither are transcript estimates, marked `est.`. A resumed session whose earlier process never wrote a `cost-state` gets that part added back as an estimate (D-21).
- **Usage limits.** bosun itself makes no model calls. During testing the account hit its Claude usage limit; bosun reports that like any other failed turn.
- **Notifications on Linux** use D-Bus through `gdbus` (post, replace and retract work). A click is handled by a short-lived child per notice with a 2 minute deadline (no daemon), so a click after that does nothing. macOS `terminal-notifier` is written but not run, and click-through there and the frontmost-app focus probe are not built (D-1). The WSL backend is not built.
- **Retention.** Session dirs are pruned seven days after the session ends (and after a later ingest); request rows older than 400 days are folded into per-day rollups, so very old history loses prompt and request detail but keeps its dollars.
- **`doctor` sees only** `~/.claude/settings.json` and the managed-policy files, not project-level plugin settings or `--plugin-dir`.
- **Hook latency** in-process is about 0.2 ms; a full process start is a few ms (about 10 ms worst case now that SQLite is linked in). Spend and fork handling rely on Claude Code's transcript layout as of 2.1.309; a layout change shows up as `est.` and a growing `unattributed`, not as silently wrong totals.
- **`display-popup -T`** needs tmux 3.3; on 3.2 the popup has no title (D-3). Everything else runs on 3.2a.

## E. The views (visual overhaul, D-27 and D-28)

Mockups are in `build-log/mockups/` (the mocks say "pitwall"; the name stays bosun).

- **Bar** (`bar.style = "labeled"`, default): `⚑ 2 waiting`, `▲ 1 runaway`, `✓ 1 done`, `$23.40 / $50`, `5 sessions · 9 agents`; zero counts are hidden. `bar.style = "compact"` is the old terse form. `ui.glyphs = "ascii"` swaps ⚑ ▲ ✓ for ! ^ +. Window tabs get the glyph after the name. ⚑ ▲ ✓ were checked single-width in tmux 3.2a. The generated conf raises `status-right-length` to 120 when it is lower; re-sourcing does not move a tab marker already placed in front of a name, so restart tmux once after upgrading.
- **prefix + W, status view.** Columns TASK, REPO, TMUX, STATE, WAIT, $ TODAY. STATE is `<state> · <detail>`; sorted needs-you (oldest first), runaway, done, working, quiet, idle. Keys: `enter` jump, `a` jump to the oldest waiting, `x` ack the row's alert (the mock's "a ack alert": `a` already meant oldest waiting, so the mock's ack is `x`), `!` interrupt (then y), `d` review diff (a window with `git diff --stat` then `git diff` in `less -R`; a custom `ui.diff_cmd` runs as given), `s` spend for the row, `v` peek, `tab` spend view, `esc`/`q` close. The footer shows only what is bound.
- **prefix + $, spend view** (`tmux.spend_key`, default `$`; it replaces tmux's rename-session binding). Left: today's total, budget bar, last 7 days, by model today, subagent share. Right: the tree task → session → prompt → request. Keys: `←→` (or `h`/`l`) fold, `b` (or `g`) group by task, day, repo, model, session; `r` (or `p`) range today, 7d, 30d; `e` export CSV; `v` reveals a prompt line when `privacy.show_text` is off; `enter` jump; `tab` status view. Request rows show the time, not a latency (the ledger has none).
- **Notices:** `bosun · needs you` / `runaway subagent` / `finished`, line 1 says what and who, line 2 where (session:window, waiting time, `bosun ack <id>`, or spend). Standard icon names. The second line is italic when the server advertises body-markup (`<i>`, not `<span>`: GNOME Shell does not render span).
- **Privacy (D-27).** `[privacy] show_text = true` (default) lets bosun show the pending command, a question and a prompt's first line, read from the transcript at render or notify time. Nothing of it is written to any file (`TestShownTextIsNeverStored`); hook event lines still never hold text. `false` restores enum-only details and notices.
- **Quiet** is its own state: purple, after working, not counted as a runaway.
- **Spend (D-28):** the gap between a session's status-line total and its priced rows is split over day, model and agent (subagent rows weigh it), marked est., instead of landing on the last activity day.
- **doctor** now warns about sessions that started before the plugin loaded and about Claude processes outside tmux, and checks the `$` binding.
