# M1b: read and display

tmux 3.2a, Claude Code 2.1.309 sessions (the binary on disk updated to 2.1.310 mid-run, D-18). Go 1.26.0. All commands `CGO_ENABLED=0`.

State at the end: `go vet ./...`, `go test ./...`, `go test -short ./...`, `make cross` green. The tmux tests are opt-in; `BOSUN_TEST_TMUX=1 go test ./...` is also green (9 tmux tests, about 10 s). Private servers only (`-L bosun-test-<pid>-<n>`, `-f /dev/null`); every server is killed and its socket file removed; the user's `default` server was never contacted.

## Files

| Path | Purpose |
|---|---|
| `internal/live` | `Session` state, event fold (`FoldFile`, `Apply`), agents.json reconciliation (D-7, D-11), `live.json` / `marks.json` I/O, `Queue` |
| `internal/eval` | the evaluator: discover and fold, kill -0 + token liveness, agents cache, detached refresh spawn, tmux pane reconcile, done-on-view, marker writes, summary |
| `internal/tmux` | 500 ms-deadline tmux client: `Panes`, `Clients`, `SetMarks` (one batched exec), `SwitchClient`, `DisplayMessage`, `Viewing` |
| `internal/segment` | the status segment (needs-you pill, runaway pill, done, working) |
| `internal/jump` | waiting-queue walk with a persisted per-client pass (`jump.json`) |
| `internal/tmuxconf` | generated `tmux.conf` (segment, tab markers, jump bind, client-attached hook) |
| `internal/platform/spawn_unix.go` | `Spawner` / `Detached` (Setsid, stdio on /dev/null, released) |
| `commands.go`, `cmd_status.go`, `cmd_jump.go`, `main.go` | command table with the single catch-up classification; `status [--tmux\|--json\|--catch-up]`, `jump` |
| `internal/install` (changed) | writes `<data>/tmux.conf`, reads the user's `tmux.conf` read-only, prints the one `source-file -q` line and where it goes |
| tests | `internal/{live,eval,tmux,jump,segment,tmuxconf,install}`, `catchup_test.go`, `tmux_integration_test.go` |
| `build-log/DECISIONS.md` | D-15 to D-18 appended |

## Acceptance

| Check | Result | Evidence |
|---|---|---|
| Count correct within 2 s across three sessions in three tmux sessions | PASS | `TestTmuxThreeSessionsCountedWithinTwoSeconds` (real tmux, real `bosun hook` and `bosun status`; counts, window marks, and segment text). Live with three real `claude --model haiku` sessions in three tmux sessions: first `status` poll showed `Agents 3`, needs-you appeared 0.8 s after the prompt was sent (timeline below) |
| Jump walks needs-you then done, oldest first, wraps | PASS | `TestTmuxJumpWalksNeedsYouThenDoneOldestFirstAndWraps` (5 presses: oldest needs-you, newer needs-you, oldest done, newer done, wrap), `TestSpecWalk` (the SPEC mockup walk), `TestWalkSurvivesDonePanesLeavingTheQueue`, `TestQueueOrder` |
| Ctrl+C on a working agent shows idle within 6 s with no manual refresh | PASS | Live: real claude working, `C-c` sent, `working` gone in 2.9 s. The evaluator spawned `bosun refresh` itself (no manual call); the spawn is unit-tested by `TestStaleAgentsCacheSpawnsRefreshOnce` and `TestCtrlCMovesWorkingToIdleViaAgents` |
| Killing a pane removes its count and marker | PASS | `TestTmuxKillingPaneRemovesCountAndMarker` (real tmux), `TestKilledPaneDropsCountAndMarker` |
| `idle_prompt` does not create needs-you | PASS | `TestTmuxIdlePromptNotificationIsNotNeedsYou`, `TestStateMachine/idle_prompt_after_stop_stays_done`; also seen live (`idle_prompt` arrived 60 s after `Stop`, state stayed done) |
| Every tmux exec has a 500 ms deadline; `kill -STOP` on the server leaves no stuck bosun | PASS | `TestSetMarksIsOneCallWithDeadline` (deadline value), `TestTmuxStoppedServerLeavesNoStuckBosun` (server SIGSTOPped, `status --tmux` exits within 2 s, next evaluation after SIGCONT still counts 3) |
| Generated conf sourced twice: `status-right` has `bosun status` once; each window format has `@bosun-win-mark` once | PASS | `TestTmuxConfSourcedTwiceIsIdempotent` (also checks the theme's own value survives) |
| `client-attached` hook idempotent (D-9) | PASS | same test: `show-hooks -g client-attached` has exactly one `status --catch-up` after two sources on 3.2a |
| Real render check, saved capture | PASS | `TestTmuxRealRenderPlain`, captures below |
| Composes with tmux-themepack | PASS | `TestTmuxRealRenderWithThemepack` sources the user's `powerline/block/blue.tmuxtheme` (read from `~/.tmux/plugins`, not modified) then the bosun conf; captures below. A second source on top of the theme leaves one segment |
| Event fold handles a partial trailing line and a 1 MB line | PASS | `TestFoldPartialTrailingLine`, `TestFoldHandlesOneMegabyteLine`, `TestFoldSkipsGarbageLines`, `TestBadTimestampLeavesStateSince` |
| D-7: needs-you leaves on two not-waiting fetches | PASS | `TestReconcileNeedsYouExit`, `TestNeedsYouLeavesAfterTwoNotWaitingFetches`; live: Esc at a permission prompt (no hook fires), needs-you gone after 6.1 s |
| D-8: background follow-up continues the turn | PASS | `TestEditCountAndTurnCarryOver`, `TestStateMachine/second_stop_with_none_is_done` |
| D-11 / D-17: background rows and agents-only sessions | PASS | `TestBackgroundRowDoesNotIdleWorkingSession`, `TestAgentsOnlySessionsAreInvisible`, `TestReconcileIdleOverWorking` |
| Catch-up wired for every user-facing command (L-26) | PASS | `TestCatchUpTable` (every command must be classified), `TestDispatchRunsCatchUpBeforeUserFacingCommands` |
| `install` prints the tmux edit, never edits `tmux.conf` | PASS | `TestInstallWritesTmuxConfAndPrintsTheOneLine`; live run against the real `~/.tmux.conf` below |
| Hooks never call tmux | PASS | `internal/hook` has no tmux import; only `internal/eval` runs tmux commands |

### Live timeline (three real haiku sessions, private server, themed bar, one attached client)

```
t+0.0s Agents 3, Working 2, Idle 1
t+0.4s Agents 3, Working 3
t+0.8s Agents 3, NeedsYou 1, Working 2        <- permission prompt (x2)
t+2.9s Agents 3, NeedsYou 1, Done 1, Working 1
t+5.3s Agents 3, NeedsYou 1, Done 1, Idle 1   <- the attached session's done went idle (viewed)
Ctrl+C on a working session: Working 1 -> Idle 1 after 2.9 s
Esc at the permission prompt: NeedsYou 1 -> 0 after 6.1 s (D-7)
```

### Real render captures (`capture-pane -p` of the outer pane; client attached from a second private server)

Plain (no theme), windows: shell working, api needs-you, web done, docs working:

```
[s1] ●0:shell*  ! 1:api  ✓2:web  ●3:docs-                                            ! 1  ✓1 ●2 "pop-os" 17:11 23-Oct-26
```

With tmux-themepack `powerline/block/blue` then the bosun conf (powerline glyphs elided by the terminal font in this capture):

```
 s1  jmvaldez  0:0                                                 ● 0:shell*  !  1:api ✓ 2:web ● 3:docs-                                                 ! 1  ✓1 ●2  17:11:08  23-Oct-26  pop-os
```

Live server, idle window with one done session elsewhere:

```
 x1  jmvaldez  0:0                                                                    0:claude*                                                                   ✓1  17:10:37  23-Oct-26  pop-os
```

`assertRendered` fails on any of `#{`, `#[`, `fg=`, `bg=`, `colour`, `#(`, or the literal `bosun status` in the rendered bar, and requires each tab marker to sit directly before its window (`! 1:api`, `✓ 2:web`, `● 3:docs`, `● 0:shell`).

## The tmux edit for this machine

`bosun install` run against the real `~/.tmux.conf` (read only; it uses tpm, themepack, prefix `C-a`) prints:

- add `source-file -q ~/.local/share/bosun/tmux.conf` after the tpm `run` line (line 91 of that file), so the theme has built `status-right` and the window formats before bosun prepends to them;
- reload once. Themepack's powerline themes already set `status-interval 1`.

`prefix + a` is unbound in that config (`C-a a` is not mapped; `C-a C-a` is `send-prefix`), so the default jump key is free. The generated conf does not bind the popup yet.

## Judgment calls

1. **Only sessions with a live pane count.** A session whose locator is empty, from another tmux server pid, or whose pane id is not in `list-panes -a` has no count, marker, or queue slot (SPEC "pane gone"). This also means a Claude Code session running outside tmux is invisible to the bar.
2. **Distinct fetches.** The two "consecutive reads" of D-7 are two distinct `agents.json` fetches (`fetched_at` differs), not two evaluator ticks, so one cache never counts twice. The idle-over-working rule needs a fetch newer than the working edge plus 1 s, no pending background tasks, and no running subagents.
3. **Late echo guard.** A `permission_prompt` Notification arriving within 30 s of its `PermissionRequest` but after the prompt was answered is ignored; otherwise it would re-enter needs-you about 6 s after the user answered.
4. **Marker values.** `@bosun-wait` holds `needs-you`, `done`, `working` (and `runaway` once rules exist); idle is unset. Markers are written in one batched tmux exec, only for windows that changed, and `marks.json` is updated only after tmux accepted the batch. A new server pid invalidates `marks.json`.
5. **Walk memory.** Visiting a done pane turns it idle and removes it from the queue, so a stateless "next after current" would lose its place. `jump.json` keeps a per-client snapshot of the pass.
6. **Empty queue message.** With a client, `bosun: nothing waiting` goes to `display-message -c <client>`; stdout from `run-shell -b` would open a view-mode pane. Without a client it prints to stdout.
7. **Detached refresh spawn** is at most once per 3 s and only while some session is tracked and not ended. `os.StartProcess` with `Setsid` is used rather than `exec.CommandContext`: the child must outlive the evaluator, and `refresh` bounds itself (lock plus 5 s runner timeout).
8. **Focus for done to idle.** A client that advertises the `focus` termfeature must also report `focused`; a client that cannot report focus counts as viewing when it shows the pane. Notification focus suppression (M3) will reuse `tmux.Viewing` but treat the no-feature case as Unknown.
9. **Discovery window.** Session dirs whose `events.jsonl` has not changed in 24 h are skipped unless already tracked, and ended sessions are forgotten after 24 h.
10. **No popup binding yet**; the `display-popup -T` gate from D-3 is noted in the generated conf for M4.
11. Test processes set `PATH=/usr/bin:/bin` so the evaluator's detached `refresh` cannot reach the real `claude`.

## Deferrals

- Runaway pill, `$!` marker, rules, notifications: M3. Spend in the segment: M2.
- Popup, `d`, peek, interrupt: M4.
- `doctor` checks for the conf header and `status-interval`: M6.
- `status --tmux` wall time under a hung tmux is bounded by the 500 ms deadline per call; with `list-panes` and `list-clients` and `set-option` that is up to 1.5 s in the worst case, inside tmux's own `#()` job model but not tight. A shared deadline across the evaluation would tighten it; not needed for acceptance.
