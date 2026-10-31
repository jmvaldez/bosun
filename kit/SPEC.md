# bosun: locked spec (rebuild edition)

bosun is a single Go binary that watches Claude Code agent sessions running across tmux. It tells you when one needs you, flags runaways, and tracks what each one costs, so you never poll windows by hand.

Terms are defined in [CONTEXT.md](CONTEXT.md). Verified Claude Code behavior is in [FACTS.md](FACTS.md). Every bug from the first build is in [LESSONS.md](LESSONS.md); the acceptance checks below already include its rules.

## 1. Goals

1. Attention: know within about 1 s when any agent session needs input.
2. Runaways: flag subagents and agent sessions over time or cost thresholds, and flag a day that is spending well above your normal pace.
3. Spend: day > task > agent session > prompt > request, subagents rolled up to their parent.
4. Notifications: desktop notice on needs-you, done, failed turn, and runaway.
5. tmux-native: a status segment, a tab marker, a popup, and a CLI. No GUI.

Scope: Claude Code agent sessions only. macOS first, Linux and WSL (with tmux) as fast follows.

## 2. Hard rules

| Rule | Detail |
|---|---|
| No daemon | No launchd agent, no socket, no listener. Short-lived detached children only. |
| Hook budget | `bosun hook` and `bosun statusline` finish under 50 ms, never block, always exit 0. |
| Hot path purity | Hooks and the status line never run `ps`, `tmux`, `git`, `claude`, or open the ledger. |
| Every exec has a deadline | Every child process bosun runs uses `exec.CommandContext` with a timeout. |
| Locks never wait forever | `flock` is `LOCK_NB`, or polled with a hard deadline (2 s max). |
| No prompt text | Never stored, logged, notified, or put in fixtures. Decoders are allowlists. The only display is the popup peek, read from the transcript at render time and dropped after the frame. |
| Pane writes are explicit | bosun sends keys to an agent pane only on popup `!` after a `y` confirmation, and only after the pane check in section 7 passes. |
| Writes only its own files | Never edits `~/.claude/settings.json`, `~/.tmux.conf`, shell rc files, or the status line script. Never installs software. The one exception is `run --worktree`, which adds a git worktree you asked for and never removes one. |
| Atomic files | Every state file is written temp file plus rename. Appends are single `O_APPEND` writes. |
| Exit codes | A hook that panics must still exit 0 (`recover` at the top of `runHook`). Exit 2 blocks a tool call. |

## 3. Architecture

### Process model

- Single static binary: `CGO_ENABLED=0`, pure-Go SQLite (`modernc.org/sqlite`), `golang.org/x/sys/unix`.
- Hot writers: `bosun hook` (per event), `bosun statusline` (per status line tick), the evaluator (inside `bosun status --tmux`, under `eval.lock`), and detached children (`bosun refresh`, `bosun notify-dispatch`) started with `Setsid` and never waited on.
- Readers: every other `bosun` command and the 1 s tmux status refresh.
- Only `bosun` CLI processes open the ledger.

### Data flow

| Source | Writer | What it gives |
|---|---|---|
| Hooks (13 events) | `bosun hook` | attention edges, pane locator, pid, start token, task, subagent pairs |
| Status line stdin | `bosun statusline` | live session total `cost.total_cost_usd`, model, version |
| `claude agents --json` | `bosun refresh` (cached, 5 s TTL) | liveness, `busy/waiting/idle`, `waitingFor` |
| Transcripts | ingest (inside `bosun` commands and the refresher) | per-request breakdown, `cost-state` at exit |
| tmux | evaluator | panes, focus, markers |

- Hooks registered: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PermissionRequest`, `PostToolUse`, `PostToolUseFailure`, `PostToolBatch`, `Notification`, `SubagentStart`, `SubagentStop`, `Stop`, `StopFailure`, `SessionEnd`. No matchers. Exec form `bosun hook --plugin-version X.Y.Z`, sync, `timeout: 5` as a backstop. Never `MessageDisplay`.
- Each event appends one allowlisted line (max 2 KiB) to `sessions/<id>/events.jsonl` and updates the hook heartbeat. `SessionStart` adds cwd, transcript path, repo, branch (read from `.git` files, no `git` exec), task, account key, `source`, and the start token of `CLAUDE_PID`.
- On a notifying edge the hook forks `bosun notify-dispatch` and exits.
- The status line wrapper writes `sessions/<id>/statusline.json`, banks on a real reset, then runs the wrapped command with the same stdin bytes and prints its output unchanged.
- The evaluator (`bosun status --tmux`, every 1 s) folds new event lines into `live.json`, reconciles with `agents.json` and `kill -0` plus start token, evaluates rules, writes changed window markers, and prints the segment. A stale `agents.json` spawns `bosun refresh` detached.
- Any other `bosun` command and a tmux `client-attached` hook run a catch-up evaluation.

### Spend truth

| Session kind | Session total comes from |
|---|---|
| Running, status line wrapped | latest `statusline.json` `cost_usd` plus `banked_usd` |
| Ended | last `cost-state.totalCostUSD` per `startTime` group, summed |
| No trusted total (pre-install, crash, `claude -p`) | transcript estimate, always marked `est.` |

- The status line total includes side requests that never reach transcripts. Transcripts are the breakdown only. The gap is shown as `unattributed`, computed at query time, may be negative.
- Ingest records session end (`ended_at`) from the `SessionEnd` event, else from last activity once the session is no longer live. A session with a total must use it; the estimate path is fallback.
- Fork inheritance is subtracted in exactly one function, `OwnUSD()`. The ledger stores gross `total_usd`, `inherited_usd`, `banked_usd`. Every view, rule, and rollup calls `OwnUSD()`.
- Day attribution: each breakdown row lands on the day of its own timestamp. The `unattributed` gap lands on the session's last activity day. Day boundaries follow `spend.day` (`local` or `utc`).
- bosun never scales breakdown rows.

## 4. Data model

### Paths

bosun resolves each dir from its own env var first, then XDG, then the default. Never Go's `os.UserConfigDir` (it returns `~/Library/Application Support` on macOS).

| Dir | Env override | XDG fallback | Default | Holds |
|---|---|---|---|---|
| Config | `BOSUN_CONFIG_DIR` | `XDG_CONFIG_HOME` | `~/.config/bosun` | `config.toml` |
| State | `BOSUN_STATE_DIR` | `XDG_STATE_HOME` | `~/.local/state/bosun` | hot files, `bosun.db`, `locks/` |
| Cache | `BOSUN_CACHE_DIR` | `XDG_CACHE_HOME` | `~/.cache/bosun` | `agents.json` |
| Data | `BOSUN_DATA_DIR` | `XDG_DATA_HOME` | `~/.local/share/bosun` | generated `tmux.conf` |
| Claude data | none (read only) | none | `$CLAUDE_CONFIG_DIR`, else `~/.claude` | transcripts |

Tests and demos set the `BOSUN_*_DIR` vars. They never set `XDG_*` for any process that runs `claude` (see LESSONS L-01).

### Hot files (state dir)

| File | Content |
|---|---|
| `sessions/<id>/events.jsonl` | allowlisted event lines: `v ts ev sid aid atype pid tmux{sock,spid,pane} prompt_id tool tuid ntype bg[] src reason` plus `SessionStart` extras |
| `sessions/<id>/statusline.json` | latest snapshot, `banked_usd`, `max_usd` |
| `sessions/<id>/tag` | task set by `bosun tag` |
| `identity.json` | account key (lowercased email or account uuid) |
| `live.json` | per session state, `state_since`, locator, pid, start token, last tool time, turn edit count, subagents (type, start, tool-call count), fold offsets |
| `marks.json` | last written marker per window |
| `today.json` | today's spend and estimated share, written by the refresher |
| `pace.json` | baseline curve for `daily_pace`, rebuilt once a day |
| `alerts.json`, `alerts.jsonl` | dedupe map `<rule>\|<scope>` with alert id and dismissed flag, and fired log |
| `notify.json` | posted groups, notifier ids, rate-limit window (flocked, atomic) |
| `heartbeat.json` | last time, plugin version, latency ring buffer per event (flocked) |

`session_id` must match `^[A-Za-z0-9-]{1,64}$` before it touches a path. Anything else: drop and exit 0.

### Ledger (bosun.db, WAL)

- `accounts(account_key PK)`.
- `sessions(session_id PK, account_key, repo, branch, task, task_source, started_at, ended_at, last_activity_at, total_usd, inherited_usd, banked_usd, forked_from, launch_session_id, trusted bool)`.
- `prompts`, `subagents`, `requests(PK message_id, request_id)`, `ingest_files(path, size, mtime, offset)`, `spend_samples(day, minute, cumulative_usd)`.
- `task_source` is `tag | env | branch | transcript`.
- Migrations run inside `BEGIN IMMEDIATE`. Ingest holds `ingest.lock` (`LOCK_NB`; skip if held).
- Upserts use plain SQL in `ON CONFLICT` (no Go-registered functions).

### Ingest rules

- Incremental by (size, mtime, offset). Advance the offset only past complete lines. Scanner buffer sized for long lines; check `scanner.Err()`.
- Files ordered by mtime ascending so a predecessor ingests before its fork.
- Skip `*.orphaned-*` and `*.superseded-*` files.
- Dedupe on `message.id` + `requestId`; keep the max `output_tokens` line (only output grows).
- Main files: skip `isSidechain` lines. Subagent files: every line is a sidechain line; ingest them all and attribute to the parent session.
- Assistant lines lack `promptId`; take the nearest preceding user line's `promptId`.
- Fork detection: a main file whose first user `uuid` exists in a known session, or a `SessionStart` with `source=fork`, or an equal first `cost-state.startTime`. Set `forked_from`, skip the copied prefix, set `inherited_usd` from the predecessor's total at fork time.
- The session that first wrote a request owns it. Never move a row.
- Banked spend from `statusline.json` is read into `banked_usd`.
- First-run ingest in the detached refresher has a time budget (10 s) and resumes next tick.
- A parse failure on one line is skipped, never fatal. Bad timestamps are zero and skipped, never `time.Now()`.

### Pricing

- Embedded table, one function `prices.Cost(model, usage)`. No second pricing path anywhere (popup, spend, rules, ledger all call it).
- Rates come from the official pricing page at build time, with the fetch date in a comment.
- Most specific model prefix first; a test covers every model id found in local transcripts.
- Cache writes priced by tier: 5 m write and 1 h write each have their own rate. Cache reads have their own rate per model (multipliers differ by model; read the page footnotes).
- Unknown model: fallback tier, row marked estimated.
- No runtime price fetch.

### Task capture

- Precedence: `bosun tag` > `BOSUN_TASK` env > key parsed from the start branch > transcript `gitBranch` (backfill only). Nothing found renders `untasked`.
- One shared parser: `task.key_regex` (default `[A-Z][A-Z0-9]+-\d+`), matched case-insensitively, result uppercased, optional `task.prefixes` allowlist. Hook, ingest, popup, and spend all use it.
- `bosun tag TEXT` and `BOSUN_TASK` take free text: whitespace trimmed and collapsed, case kept, at most 64 bytes, no control characters. Text that is wholly an issue key is uppercased. `task.key_regex` and `task.prefixes` govern only automatic detection (branch, transcript). `bosun spend --task` matches case-insensitively.
- Repo is origin `owner/name`, else the main worktree dir name.

### Retention

- `sessions/<id>/` pruned 7 days after end and ingest. Requires `ended_at` set (see ingest rules).
- `requests` kept 400 days, then pruned once rolled up. `sessions`, `subagents`, rollups kept forever.

## 5. Attention

### States

| State | Enters on | Leaves on |
|---|---|---|
| `needs-you` | `PermissionRequest`; `Notification` of type `permission_prompt`, `elicitation_dialog`, `elicitation_url_dialog`, `agent_needs_input` | `PostToolUse`, `PostToolBatch`, `Stop`, `UserPromptSubmit` |
| `working` | `UserPromptSubmit`, `PreToolUse`, background tasks pending after `Stop` | `Stop` with no background work |
| `done` | `Stop` (or `StopFailure`) with no background tasks pending | pane gets focus, then `idle` |
| `idle` | focus on `done`; `agents.json` `idle` over a stale `working` | next prompt |

- `idle_prompt` is not needs-you. Ignore it for state.
- `PreToolUse` never overrides `needs-you`.
- Episode dedupe: the `permission_prompt` Notification after a `PermissionRequest` in the same waiting episode is not a new entry.
- Hooks win over `agents.json` on a live session. `agents.json` absence plus `kill -0` plus a start token mismatch drop dead sessions.
- Subagents: `running` and `ended`, paired on `agent_id`. A stop with no matching start is ignored.
- Main vs subagent: key on `agent_id` presence, never `agent_type`.
- Display precedence: `needs-you` > `runaway` > `handoff` > `done` > `working` > `quiet` > `handed-off` > `idle`. Runaway is an active, undismissed runaway alert, not a fifth state. `handoff` is the advisory `handoff` rule on a `done` or `idle` session; `quiet` is the `quiet` rule on a `working` one (D-27, D-34). `handed-off` is a `done` or `idle` session whose `handoff.json` phase is `done`; it ranks just above `idle` in ordering, and a session in that state shows it instead of any alert (D-36).

### Rules

Evaluated by the 1 s refresh. One alert per rule and scope, with a short id. Re-arms when the condition clears.

Dismissing (`x` in the popup, `bosun ack ID` or `bosun ack --all`) hides an active alert from the segment, tab markers, popup, and Notification Center until its condition clears. It then re-arms as usual, so a dismissal never hides a later fire. Dismissal is a flag in `alerts.json`; it never changes rule state.

| Rule | Default | Scope | Fires when |
|---|---|---|---|
| `subagent_runtime` | 10m | subagent | wall time since `SubagentStart` |
| `quiet` | 5m | session | `working`, gap after `PostToolUse` with no next `PreToolUse` |
| `subagent_cost` | 2.00 | subagent | transcript estimate, always `est.` |
| `session_cost` | 10.00 | session | `OwnUSD` crosses each next multiple |
| `handoff` | 0.70 | session | `done` or `idle`, and the context fill is at least `handoff.suggest_at`; advisory, never a runaway (D-34) |
| `daily_pace` | on | account and day | see below |
| `daily_budget` | off (0) | account and day | today crosses an absolute cap |

`daily_pace`:

- Baseline days: up to the last 30 days with spend greater than zero, excluding today.
- For each baseline day, take cumulative spend at the current clock time of day. Typical = median.
- Ratio = today so far / typical.
- Amber (segment spend turns `colour214`) at ratio ≥ `pace.amber` (1.25).
- Alert once per day when ratio ≥ `pace.alert` (1.5) and today minus typical ≥ `pace.min_over` ($20).
- Silent with fewer than `pace.min_days` (7) baseline days.
- Curves come from `spend_samples` written by the refresher. For days with no samples (backfill), build the curve from request timestamps, with each session's `unattributed` gap at its last activity time.
- Day boundaries follow `spend.day`.

## 6. Notifications

| Kind | Sound | Focus-suppressed |
|---|---|---|
| needs-you | yes | yes |
| done (turn ≥ `notify.done_min_turn`) | no | yes |
| turn failed (`StopFailure`) | yes | yes |
| every runaway rule | no | no |

- Flow: hook or evaluator forks `notify-dispatch`. The child takes the notify lock (polled, 2 s deadline), re-checks the edge, checks focus, dedupes, rate-limits, renders, posts.
- All values a template needs (duration, cost, step, threshold, agent type, est flag) are passed as flags to the child. Never rendered from zero values.
- A done or failed edge for a session not yet in `live.json` still notifies.
- Catch-up alerts never notify.
- Groups: `bosun.<session>.attn`, `bosun.<session>.<rule>`, `bosun.pace`, `bosun.budget`, `bosun.overflow`. Retract when needs-you clears, done clears on focus, or the alert is dismissed.
- Rate limit: `notify.max_per_minute` (6) per rolling minute, then one self-replacing overflow notice that names the configured popup key.
- Content: title `<repo> <TASK>`, subtitle the kind, body from the fixed templates below. Never prompt text, tool input, assistant text, elicitation text, or error text.
- Click (macOS): `-activate <bundle id>` from `client_termname` (config override), plus `-execute` of `bosun jump --session <id>` with absolute bosun and tmux paths and the socket, every value shell-quoted.
- Focus: one `list-clients` call. macOS adds a frontmost-app probe (`lsappinfo front`). Linux and WSL trust the tmux `focused` flag only on a client with the `focus` termfeature. Verdict `Unknown` notifies.
- bosun never enables `allow-passthrough`.

### Templates

Every value is a number, a duration, an enum, or an `agent_type`, passed as a flag to the child.

| Kind | Body |
|---|---|
| needs-you | `waiting on you: <permission prompt \| input needed \| dialog>` |
| done | `turn finished · <N> edits · $<own> this session` |
| turn failed | `turn failed after <duration>` |
| `subagent_runtime` | `<agent_type> subagent running <duration> (limit <limit>)` |
| `subagent_cost` | `<agent_type> subagent at $<x> est. (limit $<limit>)` |
| `session_cost` | `session passed $<step>` |
| `quiet` | `no activity for <duration>` |
| `daily_pace` | `today $<x>, <ratio>x typical` |
| `daily_budget` | `today $<x> of $<cap>` |

### Backends

| Platform | Backend | Click | Retract |
|---|---|---|---|
| macOS | `terminal-notifier` (PATH, `/opt/homebrew/bin`, `/usr/local/bin`) | yes | `-remove` |
| macOS | `osascript`, opt-in only (often invisible) | no | no |
| Linux | `notify-send` (`--print-id` probed first, `--replace-id`) | when server advertises actions | `gdbus CloseNotification`, ids persisted in `notify.json` |
| WSL | `wsl-notify-send.exe`, else `powershell.exe -EncodedCommand` | no | no |

No backend found: notifications off, every other surface works, `install` and `doctor` say how to add one. WSL interop failure: retry once with `WSL_INTEROP` set to the newest socket in `/run/WSL/`.

## 7. tmux surface

### Generated conf

`bosun install` writes `<data dir>/tmux.conf` with header `# bosun X.Y.Z`. You add one line after your plugin manager and theme lines: `source-file -q <data dir>/tmux.conf`.

Rules for the generated conf (see LESSONS L-04 to L-08):

- Store bosun formats in user options with plain `set -g` (no `-F`), so they expand at render time.
- When prepending to an existing option with `set -gF` (to splice in its current value), escape every bosun part: `##(bosun status --tmux)`, `##{E:@bosun-win-mark}`.
- Inside `#{?cond,a,b}` never put commas in a style block. Use `#[fg=colour232]#[bg=colour220]`, not `#[fg=colour232,bg=colour220]`.
- Guard each prepend so sourcing the file twice adds nothing: `if -F '#{m:*bosun status*,#{status-right}}' '' "set -gF status-right '##(...)#{status-right}'"`.
- No `run` or `run-shell` wrappers around formats (`run` expands `#(` at load).
- Bind `<tmux.popup_key>` to `display-popup -E -w 90% -h 60% -T ' bosun ' '<abs bosun> ui'` and `<tmux.jump_key>` to `run-shell -b '<abs bosun> jump --queue --client "#{client_name}"'`.
- Add `set-hook -ag client-attached 'run-shell -b "<abs bosun> status --catch-up"'`.

### Status segment

Parts appear only when nonzero, on `colour233`:

| Part | Look |
|---|---|
| needs-you pill | ` ! N ` black on yellow `colour220` (1 to 2), orange `colour208` (3+) |
| runaway pill | ` $! N ` white on red `colour160` |
| handoff | `⇄N` cyan `colour51` (`<>N` with ascii glyphs) |
| done | `✓N` cyan `colour51` |
| working | `●N` grey `colour245` |
| today's spend | grey; amber `colour214` at pace amber; bold red `colour196` at pace alert or budget; `$X/$cap` when `rules.daily_budget` is set; ` est.` when the estimated share is over zero |

Empty only with zero agents and $0 today.

### Tab marker

`@bosun-wait` window option holds the window's top state. Rendered through `@bosun-win-mark`: ` ! ` yellow, ` $! ` red, `⇄` cyan, `✓` cyan, `●` grey, unset for idle. Written only by the evaluator, only on change. Hooks never call tmux.

### Popup (prefix + W, `bosun ui`)

- Native Go, raw mode via `x/sys/unix` termios (`_darwin.go`, `_linux.go`). Pad by display width.
- Header on both tabs: agent count, repo count, needs-you, runaway, done, and working counts, and today's spend (`$X/$cap` when `rules.daily_budget` is set).
- Agents tab: STATE, FOR, WHERE, TASK, DETAIL, SUBS, SPEND. Sorted by precedence, oldest first, pane-gone rows last with `enter` disabled. DETAIL shows `waitingFor`, the runaway reason, or the last tool name (never tool input). A `done` row adds the turn's edit count (`Edit`, `Write`, `MultiEdit`, `NotebookEdit` calls since the last `UserPromptSubmit`). A `working` row with an active `quiet` alert shows `quiet <duration>`.
- Subagent rows: each running subagent appears as an indented `└` row under its parent with its state glyph, `agent_type`, runtime, tool-call count, and `est.` spend. A subagent with an active runaway alert shows `$!` and renders red. Ended subagents appear only in SUBS (`running▶ total`).
- Agents keys: `enter` jumps; `s` opens the Spend tab expanded to that agent session; `d` closes the popup and opens `ui.diff_cmd` in a new tmux window at the session's cwd; `v` toggles peek; `x` dismisses the row's active alerts; `!` interrupts.
- Peek (`v`, when `ui.peek` is on): a second line under the selected row shows what the agent is doing or asking. bosun reads the tail (last 64 KiB) of that session's transcript at render time and finds the newest `tool_use` with no matching result: Bash shows the command, Edit/Write/Read show the file path, any other tool shows its first string field. One line, control characters stripped, cut to the row width. Re-read once per refresh while peek is open, held only for the frame, never written, logged, notified, or cached. On a prompt node in the Spend tree, `v` shows that prompt's first line the same way.
- Interrupt (`!`): asks `interrupt <WHERE> <TASK>? y/n`. On `y`, bosun re-checks the pane right then: the locator's server pid still matches, the pane id still exists, and `pane_pid` ancestry still holds the session's pid with a matching start token. Any mismatch refuses with `pane changed, not sent`. On a pass it runs `send-keys -t <pane> <ui.interrupt_keys>` (default `Escape`). Claude Code cannot stop one subagent alone, so `!` on a subagent row interrupts its parent's turn and the prompt says so.
- Spend tab, two panes. The summary pane shows today against the budget (a bar when `rules.daily_budget` is set, else the pace ratio), the last 7 days as one bar per day, today by model, and the subagent share. The tree pane goes task > agent session > prompt > request; `→`/`l` expands and `←`/`h` collapses. Prompts are labeled `prompt #N` plus start time, never prompt text. Request rows show model, input / output / cache tokens, and cost. A subagent is one node under the prompt that started it (type, model, request count, runtime, `est.` spend), red with an active runaway alert. Below the tree: the `unattributed` row and `TOTAL` with the estimated share. `p` cycles today, 7d, 30d; `g` cycles task, repo, session, model; `e` writes the current view as CSV to `<data dir>/exports/spend-<period>-<group>-<timestamp>.csv` (atomic) and shows the path. Below 110 columns the summary pane collapses to two lines above the tree. Footer shows pace status and "API-rate estimate".
- Hand off (`h`): on a `handoff` or `done` row it asks `hand off <task>? writes the doc, opens a new window, keeps this pane. y/n`; on a `working` row `session is mid-turn; hand off anyway (--force)? y/n`. On `y` it starts a detached `bosun handoff <sid>`, and the row shows `writing the doc` then `opening the new window` while the state file says `writing` or `launching`. When the state file says `done` the row shows `handed off` (grey, ranked just above idle) and leaves the jump queue. Every write to the pane goes through `panewrite` (D-35).
- Keys: `j/k`/arrows, `h/l` and `←/→` in the tree (`h` hands off on the status view), `enter`, `tab`, `a`, `s`, `d`, `v`, `x`, `!`, `p`, `g`, `e`, `q`/`esc`. 1 s refresh.
- Inside the popup, `TMUX_PANE` is wrong. Use `display -p '#{client_name}'` and `list-clients`.

### Jump (prefix + a)

- Waiting queue: needs-you oldest first (by time in state), then done oldest first. Runaway, working, idle and handed-off are never targets. Repeat presses walk and wrap. Empty: `bosun: nothing waiting`.
- Client from `#{client_name}`. Current pane from `list-clients`, not `display -p -c`.
- `switch-client -c <client> -t <pane>`.

### Pane gone

Locator server pid differs, or pane id missing from `list-panes -a`: no marker, no count, no queue slot, `? pane gone` in the popup. A resumed session rebinds on its next hook.

### Mockups (fake data)

Status bar, right side, with the segment prepended to an existing theme:

```
 work  me  3:0        !  1:api   $!  2:worker   ✓ 3:docs*         ! 2  $! 1  ✓1 ●1 $14.20   14:02  Mon
```

Same bar when today is running hot (spend amber, then red with an alert):

```
                                                                   ! 1  ●2 $61.75   15:40  Mon
                                                                   ! 1  ●2 $88.10   16:55  Mon
```

Agents tab:

```
┌─ bosun ──────────────────────────────────────────────────────────────────────────────────────────┐
│ [Agents]  Spend                          7 agents · 4 repos   ! 2  $! 1  ✓1  ●2   today $15.82   │
│                                                                                                  │
│   STATE          FOR      WHERE            TASK      DETAIL                     SUBS    SPEND    │
│ > ! needs-you    5m54s    work:1.0 api     ABC-142   permission prompt          1▶ 3    $3.12    │
│      ⤷ Bash: npm test -- refunds                                                              │
│      └ ● Explore          3m40s · 22 tools                                              $0.48 est│
│   ! needs-you    2m43s    home:1.0 web     XYZ-88    input needed               -       $0.94    │
│   $! runaway     14m02s   work:2.0 worker  ABC-142   subagent 14m > 10m         2▶ 2    $6.80    │
│      └ $! Explore         14m02s · 61 tools                                             $2.40 est│
│      └ ● general-purpose  2m10s · 9 tools                                               $0.35 est│
│   ✓ done         2m17s    home:1.1 web     XYZ-88    turn finished · 12 edits   0▶ 1    $1.40    │
│   ● working      3m17s    dots:1.0 shell   untasked  Bash                       -       $0.31    │
│     idle         1h01m    infra:2.0 tf     OPS-7     -                          -       $2.05 est│
│   ● working      8m22s    ? pane gone      ABC-139   pane lost                  -       $0.77 est│
│                                                                                                  │
│ j/k move  enter jump  v peek  x dismiss  ! interrupt  s spend  d diff  tab  a oldest  q close    │
└──────────────────────────────────────────────────────────────────────────────────────────────────┘
```

Spend tab (with `rules.daily_budget = 50`):

```
┌─ bosun ──────────────────────────────────────────────────────────────────────────────────────────┐
│ Agents  [Spend]                          7 agents · 4 repos   ! 2  $! 1  ✓1  ●2   today $15.82/$50│
│                                                                                                  │
│ TODAY · MON          │ TODAY  (p: today/7d/30d   g: task/repo/session/model)                      │
│ $15.82 of $50        │ NAME                                       IN / OUT / CACHE        SPEND    │
│ █████░░░░░░░░░ 32%   │ ▾ ABC-142   acme/api   2 sessions                                 $9.92    │
│ pace 0.9x by 14:02   │   ▸ work:1.0 api                                                  $3.12    │
│                      │   ▾ work:2.0 worker                                               $6.80    │
│ LAST 7 DAYS  $96.40  │     ▸ prompt #1  09:12                                            $1.60    │
│ ▅  ▂  ▁  ▇  █  ▆  ▄  │     ▾ prompt #2  09:41                                            $5.20    │
│ T  W  T  F  S  S  M  │         req  opus         18.2k / 2.1k / 412k                     $0.84    │
│                      │         req  opus          6.0k / 1.4k / 430k                     $0.62    │
│ BY MODEL             │         + 16 more requests                                        $1.34    │
│ opus      $10.41     │         $! Explore  sonnet · 38 req · 14m                         $2.40 est│
│ sonnet     $4.66     │ ▸ XYZ-88    acme/web     2 sessions                               $2.34    │
│ haiku      $0.75     │ ▸ OPS-7     acme/infra   1 session                                $2.05 est│
│ subagents    31%     │ ▸ untasked  dotfiles     1 session                                $0.31    │
│                      │   unattributed                                                    $1.20    │
│                      │ ───────────────────────────────────────────────────────────────────────  │
│                      │ TOTAL                                                    $15.82  13% est. │
│                                                                                                  │
│ pace 0.9x of typical by 14:02 (21 baseline days)    API-rate estimate                            │
│ EST. = share with no trusted session total                                                       │
│                                                                                                  │
│ j/k move  ←/→ fold  enter jump  v peek  tab switch  p period  g group  e csv  q close            │
└──────────────────────────────────────────────────────────────────────────────────────────────────┘
```

Jump walk: start on `work:3.0`, press 1 `work:1.0`, press 2 `home:1.0`, press 3 `home:1.1`, press 4 wraps to `work:1.0`.

## 8. Install and config

- Hooks ship as a Claude Code plugin in `plugin/`, with `.claude-plugin/marketplace.json` at the repo root so the repo is its own marketplace. You run `claude plugin marketplace add <repo path>` and `claude plugin install bosun@bosun`. Never also register bosun hooks in settings (they would double-fire).
- Status line: plugins cannot set `statusLine`. You edit it yourself to the guarded form `if command -v bosun >/dev/null; then exec bosun statusline -- <orig>; else exec <orig>; fi`. `bosun install` prints this string built from your current command and refuses to double-wrap.
- tmux: one `source-file -q` line (section 7), then re-source once.
- `bosun install` prints every external edit and writes only bosun-owned files: commented `config.toml` if absent, the tmux conf, `identity.json`.
- bosun execs the real `claude` binary (resolved once, absolute) for `agents --json` and `auth status --json`. `bosun run` alone goes through `$SHELL -ic` so your `<launcher>` applies, and it re-exports PATH inside the command string after the rc file runs.
- Hook decoder: typed struct with no text fields; `background_tasks` decoded to `{id,type,status,count}` only; `SessionEnd.reason` allowlisted. Stdin read with a 200 ms deadline and a 64 KiB cap. Env allowlist: `TMUX`, `TMUX_PANE`, `CLAUDE_PID`, `CLAUDE_CODE_SESSION_ID`, `BOSUN_TASK`. Unknown events and flags are ignored.
- Oversized lines: shorten string fields (256 bytes each) before marshaling; if still over 2 KiB, drop the extras block and set `truncated:true`. Never cut bytes off encoded JSON.
- Config: TOML, every key optional. A bad value falls back to its default and `doctor` reports it. Unknown keys are reported (`toml.MetaData.Undecoded`). Hooks never read config.

| Key | Default | Meaning |
|---|---|---|
| `rules.subagent_runtime` | `"10m"` | subagent wall-time alert |
| `rules.quiet` | `"5m"` | quiet gap between tool calls |
| `rules.subagent_cost` | `2.00` | subagent estimate alert, USD |
| `rules.session_cost` | `10.00` | session step, USD |
| `rules.daily_budget` | `0` | absolute daily cap, USD; `0` is off |
| `pace.enabled` | `true` | `daily_pace` rule on |
| `pace.amber` | `1.25` | segment amber ratio |
| `pace.alert` | `1.5` | alert ratio |
| `pace.min_over` | `20.00` | alert needs this many USD over typical |
| `pace.min_days` | `7` | baseline days needed before any pace output |
| `pace.window_days` | `30` | lookback for baseline days |
| `spend.day` | `"local"` | `local` or `utc` day boundaries |
| `task.key_regex` | `"[A-Z][A-Z0-9]+-\\d+"` | issue key pattern |
| `task.prefixes` | `[]` | allowed key prefixes; empty accepts any |
| `tmux.popup_key` | `"W"` | popup binding after prefix |
| `tmux.jump_key` | `"a"` | jump binding after prefix |
| `claude.bin` | `""` | empty resolves `claude` on PATH |
| `claude.launcher` | `"claude"` | what `bosun run` types inside `$SHELL -ic` |
| `notify.backend` | `"auto"` | `auto`, `terminal-notifier`, `osascript`, `notify-send`, `wsl`, `off` |
| `notify.done_min_turn` | `"20s"` | minimum turn for a done notice |
| `notify.sound` | `"default"` | sound for needs-you and failed |
| `notify.max_per_minute` | `6` | rate limit |
| `notify.activate_app` | `"auto"` | click target bundle id |
| `focus.wsl` | `"tmux"` | `tmux` or `never` (always notify) |
| `ui.diff_cmd` | `"git diff"` | what popup `d` runs in a new window at the session's cwd |
| `ui.peek` | `true` | allow popup `v` to read the pending tool call from the transcript |
| `ui.interrupt_keys` | `"Escape"` | keys popup `!` sends after confirmation |
| `run.worktree_dir` | `""` | parent dir for `run --worktree`; empty means beside the main worktree |
| `handoff.suggest_at` | `0.70` | context fill (0 to 1) at which a done or idle session is offered a handoff; `0` is off |
| `handoff.prompt` | `""` | inline override of the handoff prompt template |
| `handoff.dir` | `""` | where the handoff doc goes outside a git repo |
| `handoff.deadline` | `"5m"` | how long a handoff waits for the doc |
| `handoff.auto` | `false` | start the handoff without a key press |

### Doctor checks

`bosun doctor [--json]` is read-only (stat, never create). Exit non-zero on any fail.

- `bosun` resolves on the tmux global PATH and in a non-login `sh -c 'command -v bosun'`.
- Plugin enabled exactly once (`enabledPlugins` may be an object or an array); no bosun hooks in settings; managed policy has no `allowManagedHooksOnly` or `disableAllHooks`.
- Hook heartbeat present; p50 and p95 latency from the ring buffer under 50 ms.
- Plugin vs binary version: minor skew warns, major fails.
- `statusLine` wrapped; every live session has a fresh snapshot.
- tmux: popup support, segment present exactly once, keys bound, `status-interval` at most 5, conf header current.
- Config parses; unknown keys listed.
- Identity cached and matching current login; `claude` resolves; cache age.
- State dir exists and is writable (stat and access check only); last ingest under 20 days.
- Notifier present and permitted; passthrough duplication (passthrough on plus another notifier).
- Prompt-filter self-test: a sentinel payload through the real decoder, sentinel absent from output.
- WSL: stale `WSL_INTEROP`; state dir on `/mnt/*` fails.

### Uninstall

`bosun uninstall` prints, in order: plugin uninstall, restore the original `statusLine`, remove the tmux line and re-source, delete the binary. `--purge` deletes state, cache, data (refuses symlinks and any path outside the resolved bosun dirs); `--purge-config` also deletes config.

## 9. Platform adapters

- GOOS suffixes `_darwin.go`, `_linux.go`, shared `_unix.go`. WSL detected at runtime in the linux build (`WSL_DISTRO_NAME`, else `/proc/sys/kernel/osrelease` contains `microsoft`). One function, `platform.Detect(goos, getenv, readFile)`, used everywhere.
- `main` builds one `Adapters{Paths, Procs, Focus, Notifier, Identity, Runner, Clock}` and passes it down. No package globals.
- Procs: darwin `unix.SysctlKinfoProc("kern.proc.pid", pid)` and `kp.Proc.P_starttime`; linux `/proc/<pid>/stat` field 22. `Alive(pid, token)` treats `EPERM` as alive and rejects pid reuse. Never `ps`. Never a hardcoded struct offset.
- Focus: verdicts `Focused`, `NotFocused`, `Unknown`.
- Notifier: `Post`, `Retract`, `Capabilities{Click, Sound, Replace, Retract}`, argv through the injected `Runner` (which always applies a timeout).
- Pane fallback without `TMUX_PANE`: tty rdev match, then `pane_pid` ancestry (evaluator only).
- Linux raw mode: `unix.IoctlGetTermios` / `IoctlSetTermios`, not hand-coded constants.
- Checksums: use `shasum -a 256` on macOS, `sha256sum` on Linux, picked in the Makefile.

## 10. Privacy

- No prompt text anywhere: allowlist decoders, raw stdin never logged, `agents.json` drops `name`, fixed notification templates, fixtures redacted.
- Env read by allowlist only (hooks inherit whatever secrets your `<launcher>` injects).
- Popup rows label prompts by number and time; DETAIL and notification bodies use enums and numbers only. Peek is the one place tool input or prompt text shows: popup only, read live from the transcript, never stored, and off with `ui.peek = false`.
- Account key on every spend row, stored locally only.
- Nothing leaves the machine: no listener, no upload, no telemetry, no runtime price fetch.

## 11. Test strategy

- Unit: pure packages over injected fakes; rules with a fake clock; notifier argv through a fake `Runner`; focus verdicts over canned `list-clients` lines.
- Fixtures in `testdata/`, redacted by allowlist (keep key names, ids replaced, all text replaced with `<redacted>`).
- `TestPromptFilter`: a sentinel in every text field of every event type, including inside `background_tasks`, `tool_input`, `last_assistant_message`, `message`, `error`. Assert absent from every state file.
- `TestHookLatency`: 1000 in-process runs, fail over 40 ms p95. Skipped under `-short`; release CI runs `-short`.
- Hook contract tests: malformed, empty, 10 MB, and never-closing stdin; unwritable state dir; HOME unset; traversal `session_id`; forced panic. Each exits 0 within 300 ms.
- Replay integration: recorded events and transcripts through evaluator and ingest; assert states, alerts, spend rows, `unattributed`, fork and banked totals, day split across midnight in both `spend.day` modes.
- Opt-in, never default: `BOSUN_TEST_REAL_SESSIONS=1` (reads real transcripts read-only), `BOSUN_TEST_TMUX=1` (private `-L` server), `BOSUN_TEST_NETWORK=1`.
- Real tmux tests run on a private `-L` socket and render through a real client (see M1b).
- Cross-build job for four targets; `go vet`; `staticcheck`.

## 12. Command surface

| Command | Purpose | M |
|---|---|---|
| `hook [--plugin-version V]` (hidden) | record one hook event from stdin | M1a |
| `statusline -- CMD...` | capture totals, run the wrapped command | M1a |
| `refresh` (hidden) | refresh `agents.json`, identity, ingest, `today.json`, samples | M1a |
| `install` | print external edits, write bosun-owned files | M1a |
| `config` | print effective config | M1a |
| `status [--tmux \| --json \| --catch-up]` | evaluate; print segment, text, or JSON | M1b |
| `jump [--queue] [--session ID] [--client C] [--socket S] [--tmux PATH]` | switch to a pane | M1b |
| `spend [--by K] [--since D] [--task T] [--json \| --csv]` | spend breakdown | M2 |
| `notify --test` | post one test notice | M3 |
| `notify-dispatch` (hidden) | focus, dedupe, post or retract | M3 |
| `ack [ID \| --all]` | dismiss active alerts until they clear | M3 |
| `ui` | popup | M4 |
| `tag TEXT [--session ID] \| --clear` | set or clear a session's task | M5 |
| `run --task KEY [--dir P] [--worktree BRANCH] [-- ARGS]` | open a tasked session in a new window | M5 |
| `handoff SID\|TASK [--force] [--cancel] [--wait]` | hand a session off to a new window (D-36) | handoff |
| `backfill [--since 30d]` | ingest history | M6 |
| `doctor [--json]` | self-check | M6 |
| `uninstall [--purge] [--purge-config]` | print removal steps | M6 |

- `spend --by`: `day`, `task`, `repo`, `session`, `prompt`, `request`, `model`, `subagent`. Default `task`. An unimplemented grouping errors; it never silently aliases another.
- `jump` with no flags acts like `--queue`.
- `tag` with no `--session` targets the newest live session in `$TMUX_PANE`.
- `run` exits non-zero outside tmux; `--dir` defaults to the current pane's path.
- `run --worktree BRANCH` runs `git worktree add` (with `exec.CommandContext`) at `<run.worktree_dir>/<repo>-<branch>`, creating the branch from the current HEAD only if it does not exist. It refuses an existing path or a dirty target, never removes or prunes worktrees, and prints the path. The window opens in the new worktree, and the branch feeds task capture.
- `spend --csv` writes the same rows as `--json` as CSV to stdout. Exports stay local.

## 13. Milestones

Each milestone: build, `go vet`, `go test ./...`, cross-build, then write `build-log/<M>.md` with files, acceptance table (PASS only with evidence; anything needing a human is `BLOCKED`, never "PASS by design"), judgment calls, deferrals. The tree must build green before the log is written.

### M1a: write path

- Builds: module, Makefile (`build`, `install`, `test`), `paths`, config loader, `Adapters` (darwin), `platform.Detect`, plugin with 13 hooks, `hook`, `statusline`, `refresh` (agents cache, identity), `install`, `config`, `procs` with the sysctl start token.
- Acceptance:
  - Hook p95 under 40 ms; every hook contract test in section 11 passes.
  - `TestPromptFilter` passes, including `background_tasks`.
  - Start token is non-zero, equals `ps -o lstart=` to the second, and differs for two pids.
  - Status line output is byte-identical to the unwrapped command (test with a real script).
  - Banking fires only on a real reset (new < 50% of max, or new < $0.01), not on a small dip.
  - Paths honor `BOSUN_*_DIR`; nothing under real `~/.local` changes during tests.
  - Status line stdin and subagent pair fixtures captured (CHECKLIST) or marked `BLOCKED`.

### M1b: read and display

- Builds: evaluator, `live.json`, `status --tmux`, markers, `jump`, generated tmux conf with jump key and `client-attached` hook, detached `refresh` spawn on a stale cache.
- Acceptance:
  - Count correct within 2 s across three sessions in three tmux sessions.
  - Jump walks needs-you then done, oldest first, wraps.
  - Ctrl+C on a working agent shows idle within 6 s with no manual `refresh` (spawn is wired).
  - Killing a pane removes its count and marker.
  - `idle_prompt` Notification does not create needs-you.
  - Every tmux exec has a 500 ms deadline; `kill -STOP` on the tmux server leaves no stuck bosun.
  - Generated conf sourced twice: `show -gv status-right` contains `bosun status` exactly once, and each window format contains `@bosun-win-mark` exactly once.
  - Real render check: start the test server, attach a client to it from inside a second isolated server, `capture-pane -p` the outer pane, and confirm the segment and tab markers render as text (no raw `#{`, no `bg=colour`, no stray commas). Save the capture to the build log.
  - Event fold handles a partial trailing line and a 1 MB line without skipping events.

### M2: ledger and spend

- Builds: schema, migrations, price table, ingest, `OwnUSD()`, session end recording, `unattributed`, refresher ingest every 10 min plus `today.json` and `spend_samples`, segment spend, `spend`.
- Acceptance:
  - Ended sessions: `OwnUSD` equals `/cost` (or the status line) to the cent (CHECKLIST).
  - A session with a `cost-state` total is trusted; `ended_at` is set for every ended session.
  - `spend --by prompt` rows plus `unattributed` equal `OwnUSD`; rows priced per their own model.
  - Fork: parent once, fork own spend excludes inherited, subtraction happens in one function (grep shows one call site computing it).
  - `/login` reset keeps banked spend in the ledger.
  - Subagent requests are ingested and attributed to the parent.
  - Re-ingest is a no-op; concurrent `spend` and `refresh` do not corrupt task or account sources.
  - Day split: a session crossing midnight splits by row timestamps; switching `spend.day` moves rows between days as expected.
  - Price test covers every model id in local transcripts (opt-in) and every table entry's ordering.

### M3: rules and notifications

- Builds: rules engine (fake clock) including `daily_pace`, alert state, runaway pill and marker, catch-up, hook fork to `notify-dispatch`, dispatch, backends, templates, `notify --test`.
- Acceptance:
  - Every alert template renders real values (test each rule).
  - `daily_pace`: silent under 7 days; amber at 1.25x; one alert at 1.5x and $20 over; no second alert the same day; correct across a `spend.day` switch.
  - Notify lock is non-blocking with a deadline; a hung backend (fake `Runner` that sleeps) does not stall the next dispatch.
  - Rate limit: 10 alerts give 6 notices plus one overflow.
  - `notify.json` writes are atomic.
  - `ack` and popup `x` hide an alert from segment, markers, and popup and retract its notice; the same alert re-fires after its condition clears and re-trips.
  - Live checks in CHECKLIST (needs-you, focus suppression, retract, done threshold, click).

### M4: popup

- Acceptance:
  - Rows match `status --json`; `enter` jumps; pane-gone rows disabled; `p` and `g` cycle.
  - SPEND column equals `spend --by session` for the same session.
  - Subagent rows match `live.json` (type, runtime, tool count); a runaway subagent row is red; ended subagents only count in SUBS.
  - A `done` row's edit count matches the turn's edit tool calls in a replay fixture.
  - Spend tree: expand and collapse work; prompt and request rows equal `spend --by prompt` and `--by request` for the same session; subagent nodes sit under the prompt that started them; the by-model summary sums to TOTAL.
  - `s` opens the Spend tab on the selected session; `d` opens `ui.diff_cmd` in a new window at the session's cwd (private tmux server).
  - Header counts equal the segment counts.
  - `TestPromptFilter` sentinels never appear in either tab with peek off; with peek on they appear only on the peek line and never in any file under the bosun dirs.
  - Peek shows the pending `tool_use` from a fixture transcript (Bash command, file path, first string field); a 10 MB transcript reads only the tail.
  - Interrupt: `n` sends nothing; `y` sends `ui.interrupt_keys` to the right pane on a private server; a changed server pid, missing pane, or start token mismatch refuses with nothing sent.
  - `e` writes a CSV whose rows and total equal `spend --csv` for the same period and group.
  - Empty state reads "No Claude Code agents running" with a working Spend tab.
  - Below 110 columns the summary pane collapses above the tree.
  - Captures of both tabs, wide and narrow, saved.

### M5: task capture

- Acceptance: `run --worktree feat/ABC-123` creates the worktree beside the repo, opens the window there, and tags `ABC-123`; it refuses an existing path. `run --task ABC-123` opens a named window, the session shows `ABC-123`, the window closes on exit, hooks fire (PATH survived the rc file); `tag` overrides a branch key; `run` outside tmux exits non-zero; the hook, ingest, popup, and spend views agree on every task.

### M6: backfill, doctor, uninstall

- Acceptance: `backfill` twice yields identical ledgers; `doctor` passes on a full install and fails on a disabled plugin, double registration, and an unwrapped status line; `doctor` leaves the filesystem unchanged (compare a tree listing before and after); retention prunes ended sessions; `uninstall` prints steps in order; `--purge` refuses a symlinked dir.

### M7: Linux

- Acceptance: linux CI green (procs, tmux on a private socket, notifier argv); M1a to M6 checks that need no desktop pass in a Linux VM or container; `notify-send` retract works across two dispatch processes.

### M8: WSL

- Acceptance (manual, CHECKLIST): focus drop on alt-tab, both toast paths from a detached child in an old tmux server, interop retry, `/mnt/*` doctor failure.

### M9: distribution

- Builds: `make release` (four tarballs plus checksums in gitignored `dist/`), release workflow on `v*` tags, optional formula for a personal tap, version stamping (`-X main.version`, fallback to build info `vcs.revision` plus `-dev`).
- Acceptance: a clean macOS user account installs with `make install` plus the plugin commands and `doctor` passes; a release tarball runs `--version` on Linux.

### After M9

A review pass with independent verification (PROMPT.md), then a demo script.

## 14. Out of scope

- Non-Claude agents; Neovim integration; native Windows without tmux.
- Any team view, upload, or org telemetry. Local CSV from `spend --csv` and the Spend tab is in scope.
- `reconcile` against an external source. Dropped from v1: the status line total already matches Claude Code's own figure to the cent, and every external source needs either a collector process (breaks no-daemon) or an org key. The ledger keys on `session_id` and `request_id`, so a later `reconcile --source otel-file <path>` reading an OTLP file exporter can join without a schema change.
- `subagentStatusLine` as a cost source (it has no cost field).
- Window-name task parsing, issue tracker lookups, prompt seeding in `run`, worktree removal.
- Stopping a single subagent (Claude Code has no per-subagent interrupt).
- Exact billing. Every dollar figure is an API-rate estimate, including for subscription plans.
- tmux-hub or any screen-scraping state source.

## 15. Future

- `bosun export sessions` (session to task, repo, branch; no cost, no paths).
- `reconcile --source otel-file`.
- A tpm plugin wrapper for the tmux conf.
- Daily summary.
