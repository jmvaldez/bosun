# Lessons from the first build

Every bug the first build hit, as a rule plus the test that catches it. SPEC.md already folds these into its acceptance checks; this file explains why each rule exists.

Format: Symptom. Root cause. Rule. Test.

## Isolation and environment

**L-01. A demo broke the real `claude` command.**
The demo set `XDG_DATA_HOME` for the whole tmux server so bosun would use temp dirs. Claude Code's installer and updater also honor XDG, so it installed a new version under the demo dir and repointed `~/.local/bin/claude` there. Deleting the demo dir would have broken `claude`.
Never set `XDG_*` for any process that runs `claude`. bosun reads its own `BOSUN_STATE_DIR`, `BOSUN_DATA_DIR`, `BOSUN_CONFIG_DIR`, `BOSUN_CACHE_DIR`.
Test: the demo script's `down` step checks `readlink ~/.local/bin/claude` is unchanged from `up`.

**L-02. Hooks silently failed with "Executable not found in PATH: bosun".**
`bosun run` launched `$SHELL -ic '<launcher> ...'`. The interactive rc file reset PATH, dropping the dir that holds `bosun`. Hooks inherit Claude's env, so every hook exited 127 and events were lost.
Re-export PATH inside the command string, after the rc file runs: `export PATH=<bosun dir>:$PATH; <launcher> ...`. `doctor` checks `command -v bosun` in a non-login shell.
Test: M5 live check that hooks fire in a `run` window; `doctor` unit test with a PATH lacking bosun.

**L-03. Tests or demos touched real state.**
Early commands used real XDG dirs when an env var was forgotten.
Every test uses a temp root through the `BOSUN_*_DIR` vars. Tests that read real transcripts, drive real tmux, or hit the network are opt-in through `BOSUN_TEST_REAL_SESSIONS`, `BOSUN_TEST_TMUX`, `BOSUN_TEST_NETWORK`.
Test: CI runs with HOME set to a temp dir; a guard test fails if any path resolves under the real HOME.

## tmux

**L-04. Tab markers rendered as garbage (`bg=colour220,bold] ! ,bg=colour160 ...`).**
The generated conf used `set -gF` on the marker format. `-F` expands at set time, so the conditional was evaluated once and its pieces were spliced in as literal text.
Never use `-F` on a format that must expand at render time. Store bosun formats in user options with plain `set -g`. When `-F` is needed to splice in an existing value, escape bosun parts as `##{...}` and `##(...)`.
Test: M1b real render check.

**L-05. Commas inside style blocks broke conditionals.**
`#{?cond,#[fg=a,bg=b] ! ,}` splits on the commas inside `#[...]`.
Inside `#{?...}` use one attribute per block: `#[fg=a]#[bg=b]#[bold]`.
Test: a unit test that parses every generated conditional and asserts no comma inside `#[...]` within it; plus the render check.

**L-06. The bosun segment vanished from `status-right`, or doubled on reload.**
The prepend ran before the theme rebuilt `status-right`, or ran again on each `source-file`.
Source the bosun conf after the plugin manager and theme. Guard every prepend with `if -F '#{m:*bosun status*,#{status-right}}'` so sourcing twice is a no-op.
Test: source twice, assert exactly one occurrence in `status-right` and each window format.

**L-07. Format checks passed while the real bar was broken.**
Early verification evaluated format strings with `display -p`, which does not match what a client draws.
Verify by capturing a real rendered status bar: run the test server, attach a client to it from a pane in a second isolated server, then `capture-pane -p` that pane.
Test: M1b acceptance; capture saved in the build log.

**L-08. A server check used `tmux info`.**
`info` does not exist in tmux 3.5.
Use `tmux -L <name> has-session` (exit code) to check a server.
Test: integration helper uses `has-session`; grep in CI for `tmux info`.

**L-09. A hung tmux server froze the evaluator.**
`runTmux` had no deadline and ran under `eval.lock`. Every later tick failed the lock and served stale data.
Every tmux exec gets a 500 ms context timeout.
Test: `kill -STOP` the server during `status --tmux`; no bosun process remains after 2 s.

**L-10. Jump went to the wrong pane from a popup or key binding.**
Inside `display-popup`, `TMUX_PANE` names a pane that is not in `list-panes`. `display -p -c <client>` resolves the default target, not the client's pane.
Inside the popup use `#{client_name}`; get the client's current pane from `list-clients`.
Test: integration test opens a popup on a private server and checks the resolved pane.

## Process and locking

**L-11. Darwin start token was all zeros, so pid reuse was never caught.**
The code read `kinfo_proc` at a hardcoded offset 168 (`p_uticks`, always 0). `p_starttime` is at offset 0. The build log claimed "verified against XNU source"; nobody ran it.
Use `unix.SysctlKinfoProc("kern.proc.pid", pid)` and `kp.Proc.P_starttime`. Never hardcode struct offsets.
Test: token non-zero, equal to `ps -o lstart=` to the second, different for two live pids.

**L-12. notify-dispatch children piled up.**
A blocking `flock(LOCK_EX)` plus `lsappinfo` and `tmux` execs with no timeout. One hung `lsappinfo` after sleep stalled every later child.
The `Runner` always uses `exec.CommandContext` with a timeout (1 to 2 s). Locks are `LOCK_NB` or polled with a 2 s deadline.
Test: fake `Runner` that sleeps 30 s; the next dispatch still completes.

**L-13. The agents cache never refreshed; Ctrl+C left sessions stuck in `working`.**
The stale flag from the cache read was discarded and the detached refresh had zero callers.
A stale cache read spawns `bosun refresh` detached (deduped by `refresh.lock`).
Test: stale cache triggers exactly one spawn; a written `fake` cache moves `working` to `idle`.

**L-14. Concurrent ingest overwrote task and identity sources.**
Ingest took no lock, and the upsert's `ON CONFLICT` called Go functions that modernc SQLite never registered, so every upsert fell back to an unlocked read-modify-write.
Ingest holds `ingest.lock` (non-blocking, skip if held). `ON CONFLICT` uses plain SQL `CASE`. Migrations run in `BEGIN IMMEDIATE`.
Test: run `spend` and `refresh` concurrently on a fresh DB 20 times; sources and row counts stable.

**L-15. Heartbeat latency stats were always one sample.**
The samples field was unexported, so it never round-tripped through JSON, and the read-modify-write was unlocked.
Export and tag the ring buffer; write under a flock.
Test: 100 hook runs yield 100 samples (capped ring) in `heartbeat.json`.

**L-16. First-run ingest in the detached refresher could run for minutes.**
Time-budget detached ingest (10 s); resume on the next tick.
Test: ingest over a large synthetic tree returns within budget and finishes over later runs.

## Hook contract

**L-17. A hook blocked for 5 s.**
`io.ReadAll(stdin)` had no deadline; a stalled writer held it until Claude Code's timeout.
`SetReadDeadline(now+200ms)` on stdin plus `io.LimitReader(64 KiB)`.
Test: never-closing stdin returns in under 300 ms with exit 0.

**L-18. A panic would block a tool call.**
A Go panic exits 2, and exit 2 on `PreToolUse` blocks the tool and feeds the stack trace to Claude.
`defer recover()` at the top of `runHook` and `runStatusline`, then exit 0.
Test: a test-only flag forces a panic; exit code is 0.

**L-19. A crafted `session_id` wrote outside the state dir.**
`"../../evil"` created a directory outside `sessions/`.
Validate `^[A-Za-z0-9-]{1,64}$` before building any path; else drop and exit 0.
Test: traversal id writes nothing outside the temp root.

**L-20. Oversized lines became invalid JSON.**
Truncation chopped bytes off encoded JSON, so eval and ingest dropped the line, losing the pane, pid, and start token.
Cap string fields at 256 bytes before marshaling; if still too long, drop the extras block and set `truncated:true`.
Test: a `SessionStart` with 5 KB cwd and branch yields valid JSON with locator intact.

**L-21. A text-capable field was stored raw.**
`background_tasks` was stored as raw JSON. Today it holds enums, but any future text field would land verbatim. The sentinel test never put a sentinel there.
Decode `background_tasks` to `{id,type,status,count}`. Allowlist `SessionEnd.reason`. The sentinel test covers every nested field.
Test: `TestPromptFilter` with sentinels inside `background_tasks[].description` and `.command`.

**L-22. The event fold skipped events.**
Default 64 KiB scanner buffer, no `scanner.Err()` check, and the offset came from the file position, so a partial trailing line or a long line advanced past unprocessed events.
Size the buffer, check `Err`, advance only by complete lines consumed.
Test: fold a file with a partial last line, then complete it; every event is applied once.

**L-23. A bad timestamp reset timers.**
`parseTS` returned `time.Now()` on failure, resetting `state_since` and the quiet timer.
Return zero time and skip updates on zero.
Test: a malformed `ts` line leaves `state_since` unchanged.

## Attention

**L-24. `idle_prompt` must not count as needs-you.**
It fires about 60 s after a turn ends with no input. Treating it as blocking flags every idle agent.
Needs-you comes only from `PermissionRequest` and Notification types `permission_prompt`, `elicitation_dialog`, `elicitation_url_dialog`, `agent_needs_input`.
Test: replay with an `idle_prompt` after `Stop`; state is `done`, not `needs-you`.

**L-25. `agent_type` misclassified main-agent events as subagent events.**
With an `agent` setting, `agent_type` appears on every main-agent event.
Key main vs subagent on `agent_id` presence only.
Test: fixture with `agent_type` on main events and no `agent_id` keeps them on the session.

**L-26. Catch-up evaluation was defined but not called.**
It was wired into one command, then deferred milestone to milestone.
Every user-facing command plus the tmux `client-attached` hook runs catch-up. List them in one place in `main`.
Test: a table test over commands asserts catch-up ran.

**L-27. A short session's done notice was dropped.**
Dispatch required the session in `live.json`, which a sub-second session never reached.
For done and failed edges, a missing live entry means the edge holds.
Test: dispatch with an empty `live.json` still posts.

## Spend

**L-28. The trusted total path was dead code.**
Ingest never set `ended_at`, so every session fell back to the transcript estimate even when a `cost-state` total matched Claude Code to the cent. Retention pruning also depended on it.
Record session end at ingest (from `SessionEnd`, else last activity once not live). Any session with a total uses it.
Test: ingest a fixture with a `cost-state`; `ended_at` set and `trusted` true.

**L-29. bosun undercounted real spend by about a quarter.**
Side requests (summaries, prompt suggestions, compaction, web fetch handling) never reach transcripts, and transcript `output_tokens` often stop short of the final value.
The status line total and `cost-state` are the session truth. Transcripts are breakdown only; the gap is `unattributed`.
Test: a fixture where `cost-state` exceeds the row sum shows a positive `unattributed` and the right total.

**L-30. Fork spend was counted twice, then nearly subtracted twice.**
Fork fields were declared but never assigned. After the fix, ingest stored a net total and the day query subtracted `inherited_usd` again.
Store gross. Subtract inheritance in one function, `OwnUSD()`, used by every view and rule.
Test: parent $10, fork total $12 with $10 inherited: parent 10, fork 2, sum 12. A grep test asserts one definition of the subtraction.

**L-31. Fork prefix ownership depended on readdir order.**
Alphabetical order could ingest the fork before its parent, giving the fork the parent's rows.
Sort files by mtime ascending before ingest.
Test: fixture names chosen so alphabetical order is wrong; ownership is still correct.

**L-32. Subagent spend was zero.**
The subagent ingest path skipped `isSidechain` lines, and every subagent line is a sidechain line.
Skip sidechain lines only in main files.
Test: parent plus subagent fixture attributes every subagent request to the parent.

**L-33. Output tokens undercounted by half.**
Only `output_tokens` grows across a request's lines; taking the first line loses most of it.
Dedupe on `message.id` + `requestId` keeping the max output line.
Test: a fixture with three lines per request uses the largest.

**L-34. Prices were wrong in several ways.**
A newer model family was priced at an older generation's rates (3x high). A broad prefix matched older variants with different rates. Some 1 h cache write rates were set to the 5 m value. A second copy of pricing logic lived in the ledger. The popup used a flat per-token rate. `--by prompt` priced every row at the fallback rate.
One pricing function. Rates from the official page with the fetch date. Most specific prefix first. Every caller passes the row's own model.
Test: rate table test per entry; ordering test; popup and `spend` agree for the same session.

**L-35. Banking doubled live cost after a small dip.**
Any decrease in the status line total triggered banking.
Bank only on a real reset: new < 50% of the max, or new < $0.01.
Test: 5.00 then 4.98 does not bank; 5.00 then 0.02 banks 5.00.

**L-36. Evening sessions vanished from today.**
Local day strings were compared with UTC timestamps, and the day query filtered on `started_at`, so a session that started yesterday and ran into today added $0.
Convert day bounds to UTC instants. Attribute rows by their own timestamps; unattributed on the last activity day. Offer `spend.day = local | utc`.
Test: a session spanning midnight, in both modes, with TZ set to a negative and a positive offset.

**L-37. Provider usage pages disagreed with bosun day by day.**
Some usage pages use the UTC day and their daily bars lag by hours, while month-to-date is current.
Offer the UTC day setting. Compare against month-to-date or `/cost`, not today's bar.
Test: manual (CHECKLIST).

## Notifications

**L-38. Every alert banner read "$0" or "0s".**
The evaluator forked dispatch with only session and rule; template values were zero-value locals.
Pass every template value as a flag to the child.
Test: render each rule's template from parsed flags; each body contains the real number.

**L-39. `-execute` broke on paths with quotes.**
The click command was built with unquoted interpolation.
Shell-quote every value in `-execute`.
Test: a bosun path containing an apostrophe round-trips through `sh -c`.

**L-40. Linux retract never fired.**
Notification ids lived in process memory, but each dispatch is a new process. `--print-id` was used without checking support.
Persist ids in `notify.json` under the lock. Probe `--print-id` once and cache the capability.
Test: post in one process, retract in another, through the `fake` Runner.

**L-41. Rate-limit state could reset.**
`notify.json` was written in place.
Temp file plus rename for every state file.
Test: a grep test for direct `os.WriteFile` on state paths outside the atomic helper.

**L-42. osascript notifications reported success and showed nothing.**
macOS attributes them to Script Editor, which may be set to no banners; clicks open Script Editor.
osascript is opt-in only. Default backend is terminal-notifier; missing means notifications off.
Test: `Resolve("auto")` never picks osascript.

## Config, doctor, install

**L-43. Typo keys were ignored silently.**
TOML metadata was discarded.
Report `MetaData.Undecoded()` in `doctor` and `config`.
Test: a config with `rules.quite` reports one unknown key.

**L-44. `doctor` created the state dir.**
A writability check called `MkdirAll`.
Doctor is read-only: stat and access checks only.
Test: tree listing identical before and after `doctor`.

**L-45. Plugin detection failed on one settings shape.**
`enabledPlugins` appears as an object `{"name@market": true}` in some settings and as an array in docs.
Accept both.
Test: doctor tests for both shapes.

**L-46. `--purge` could follow a symlink.**
Refuse symlinked dirs and any path outside the resolved bosun dirs.
Test: a symlinked state dir is not deleted.

**L-47. A spend grouping silently returned another grouping.**
`--by subagent` aliased `--by session` with a bare TODO.
Unimplemented options error. TODOs reference an issue.
Test: every `--by` value returns its own shape.

## Process lessons

**L-48. "PASS by design" and "PASS (unit tested)" hid live bugs.**
Several acceptance rows passed on reasoning alone; the review then found the feature broken.
A live check without live evidence is `BLOCKED`, never `PASS`.

**L-49. Parallel edits left the tree red.**
One milestone's fixer saw build errors from another's in-progress work and logged them as "outside scope".
One writer per tree at a time. The tree builds and tests green before a build log is written.

**L-50. Build flags drifted.**
Some logs built with `CGO_ENABLED=1`.
Every build, test, and doc command uses `CGO_ENABLED=0`, through the Makefile.

**L-51. The hook latency test failed release CI on loaded runners.**
Skip `TestHookLatency` under `-short`; release CI runs `-short`; the regular CI job runs it.
