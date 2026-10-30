# M1a: write path

Built on Linux (Pop!_OS 22.04, tmux 3.2a, Go 1.26.0 via `GOTOOLCHAIN=auto`). Claude Code 2.1.309 for every live capture. Darwin code compiles and vets for amd64 and arm64; its live checks are `BLOCKED (macOS)` per D-1.

Final state: `go vet ./...`, `go test ./...` (97 test funcs and subtests pass, none skipped), `go test -short ./...`, and `make cross` (linux and darwin, amd64 and arm64: build plus vet) are green. All commands run with `CGO_ENABLED=0`. `staticcheck` is not installed on this machine, so it was not run.

## Files

| Path | Purpose |
|---|---|
| `go.mod`, `go.sum`, `VERSION`, `Makefile`, `.gitignore` | module `github.com/jmvaldez/bosun`, `go 1.26` + `toolchain go1.26.0`; `VERSION` is embedded by `main.go` and checked against both plugin manifests; Makefile has `build`, `install` (temp file + `mv`, not run), `test`, `vet`, `cross`, `check` |
| `main.go` | adapter wiring and command dispatch: `hook`, `statusline`, `refresh`, `install`, `config`, `version` |
| `internal/paths` | `BOSUN_*_DIR` > absolute `XDG_*_HOME` > default; `ValidSessionID` |
| `internal/config` | TOML loader, 28 keys from SPEC section 8, table-driven (load, format, known keys from one table) |
| `internal/platform` | `Detect`, `Adapters`, `Procs` (`procs_linux.go` primary, `procs_darwin.go`, shared `procs_unix.go`), `ClaudeIdentity`, `Clock`; `Focus` and `Notifier` are interfaces only |
| `internal/hook` | allowlist streaming decoder, event schema, size caps, append, `Run` with `recover` |
| `internal/statusline` | snapshot, `Bank`, byte-exact passthrough |
| `internal/refresh` | `agents.json` cache (D-11 rows), identity file, `Run` under `refresh.lock` |
| `internal/install` | printed edits, `WrapCommand`, commented `config.toml`, `identity.json` |
| `internal/heartbeat`, `flock`, `atomicfile`, `stdin`, `runner`, `clean`, `gitinfo`, `task` | shared pieces |
| `plugin/.claude-plugin/plugin.json`, `plugin/hooks/hooks.json`, `.claude-plugin/marketplace.json` | plugin with 13 exec-form hooks, repo-as-marketplace |
| `scripts/redact.py` | CHECKLIST redaction (key names and numbers kept, ids to stable placeholders, text to `<redacted>`) |
| `testdata/` | `statusline-stdin.json`, `hook-subagent-pair.jsonl`, `hook-subagent-session.jsonl`, `hook-notification.jsonl`, `agents-json.json` (all redacted) |

## Acceptance

| Check | Result | Evidence |
|---|---|---|
| Hook p95 under 40 ms | PASS | `TestHookLatency` (1000 in-process runs): p50 160 us, p95 210 us, max 9.8 ms. `TestHookProcessWallTime` (20 full-process runs, runtime start included): worst 2.1 ms |
| Hook contract: malformed, empty, 10 MB, never-closing stdin, unwritable state dir, HOME unset, traversal id, forced panic; each exits 0 within 300 ms | PASS | `TestContract/*` in-process and `TestHookContractSubprocess/*` against the built binary (stdout also asserted empty). Panic case uses a clock that panics, caught by the `recover` in `hook.Run` |
| `TestPromptFilter` incl. `background_tasks` | PASS | `internal/hook` `TestPromptFilter`: sentinel in every F3 text path of all 13 events (`prompt`, `tool_input`, `tool_response`, `tool_calls[]`, `error`, `last_assistant_message`, `message`, `title`, `background_tasks[].description/.command/.agent_type`, `permission_suggestions[].directories`, `scratchpad_dir`, `session_crons`, `agent_transcript_path`, `model`, `session_name`); whole temp tree scanned. Mutation check: wiring `last_assistant_message` into `tool_name` made it fail |
| Start token non-zero, equals `ps -o lstart=` to the second, differs for two pids | PASS (Linux) | `TestAliveSelfAndToken`, `TestStartTimeMatchesPS` (linux), `TestTokensDifferAcrossProcesses`, `TestParseStartTicksAwkwardComm` (comm `a b) c`). Darwin: BLOCKED (macOS), see below |
| Status line output byte-identical to the unwrapped command | PASS | `TestOutputByteIdentical` (ANSI, NUL, 0xff, no trailing newline, stderr kept separate), `TestStatuslineSubprocessByteIdentical` (binary). Live: the user's real `statusLine.command` from `~/.claude/settings.json` (read-only), wrapped by `bosun install`'s printed string, run under `sh -c` on the redacted fixture: `cmp` identical (28 bytes), also identical with `bosun` absent from PATH |
| Banking only on a real reset | PASS | `TestBank` (5.00 to 4.98 no bank; 5.00 to 0.02 banks 5.00; just-above-half no bank; second reset accumulates), `TestSequenceOnDisk` |
| Paths honor `BOSUN_*_DIR`; nothing under real `~/.local` changes | PASS | `TestResolveOverrideBeatsXDG` etc.; every test uses temp roots; before/after check below |
| Status line stdin, subagent pair fixtures | PASS | `testdata/statusline-stdin.json`, `testdata/hook-subagent-pair.jsonl` (plus `hook-subagent-session.jsonl`), `testdata/hook-notification.jsonl`; replayed by `TestReplaySubagentFixture`, `TestReplayNotificationFixture`, `TestCapturedStdinFixture` |
| Plugin: 13 events, exec form `bosun hook --plugin-version X.Y.Z`, sync, timeout 5, no matchers | PASS | `TestPluginHooksJSON`, `TestManifestVersionsMatchVersionFile`; `claude plugin validate ./plugin` and `claude plugin validate .` both report "Validation passed". Live: `claude --plugin-dir ./plugin` in a private tmux pane wrote `events.jsonl`, `heartbeat.json`, `statusline.json` under temp `BOSUN_*_DIR`s (see Captures) |
| `refresh` D-11 background rows | PASS | `TestParseAgentsRows`, `TestLiveAndIdleSignalForBackgroundRows`, `TestParseCapturedAgentsFixture`; live `bosun refresh` against the real `claude agents --json` wrote 3 rows (2 background, 1 pid) and `identity.json`, no `name` stored |
| `install` prints external edits, writes only bosun-owned files | PASS | `TestInstallWritesOnlyOwnedFilesAndPrintsEdits`, `TestInstallSubprocessLeavesUserFilesAlone`, `TestInstallRefusesToDoubleWrap`; live run against the real settings.json (unchanged, mtime and size checked) |
| `config` reports unknown keys | PASS | `TestUnknownKeyReported`, `TestConfigCommandReportsUnknownKeys` |
| Darwin start token live | BLOCKED (macOS) | On a Mac: `go test ./internal/platform -run TestStartTimeMatchesPS -v` (needs a Mac; the test is `procs_darwin_test.go`) |
| Darwin build | PASS (compile only) | `make cross`, `GOOS=darwin GOARCH=amd64 go test -c ./internal/platform` |

### Isolation check

| Item | Before | After |
|---|---|---|
| `~/.local/state/bosun`, `~/.local/share/bosun`, `~/.config/bosun`, `~/.cache/bosun` | absent | absent |
| `~/.local/bin/bosun` | absent (`make install` not run) | absent |
| `~/.claude/settings.json` (mtime, size) | 1788389282, 4210 | 1788389282, 4210 |
| `~/.tmux.conf` | 3421 bytes, Sep 2 08:17 | unchanged |
| Real tmux server (`default`) | untouched | untouched; private server `bosun-test-1` only, killed, stale socket removed |
| `readlink ~/.local/bin/claude` | `.../versions/2.1.309` | **`.../versions/2.1.310`** |

The `claude` symlink moved. I did not run an installer and set no `XDG_*` variable. The live panes showed Claude Code's own banner "current: 2.1.309 - latest: 2.1.310 - Update installed - /restart to apply" while my captures ran, so the built-in auto-updater did it (it runs for any interactive start, so it would have happened on the owner's next `claude` too). Nothing was repointed to a temp dir (the L-01 failure), but `down`-style "symlink unchanged" checks in future demos must tolerate this, and later captures will run on 2.1.310. The versions dir now holds 2.1.300, .302, .306, .309, .310.

## Captures (live, private tmux server `bosun-test-1`, `--model haiku`, cwd `/tmp/bosun-cap`)

Each pane ran `claude --settings <json> --plugin-dir ./plugin` through an env-stripping wrapper (`CLAUDE_*` and `TMUX*` removed), with `BOSUN_*_DIR` under the scratchpad and `bin/` on PATH. A raw recorder hook captured payloads next to the real bosun plugin. Raw captures stay in the scratchpad; only redacted files are in `testdata/`.

- **`CLAUDE_CODE_ENTRYPOINT` (D-10).** Interactive: `cli`. `claude -p`: `sdk-cli`. Both read from the hook process env; bosun's `SessionStart` event stored them as `start.entrypoint`. So "non-interactive" = value starts with `sdk-`.
- **Esc at a permission prompt.** Zero hook events for 15 s afterwards (FACTS-CHECK listed this as BLOCKED). It confirms the D-7 gap: `agents.json` is the only exit signal.
- **Notification types.** Interactive `permission_prompt` arrived after `PermissionRequest`; `idle_prompt` arrived about 70 s after `Stop`. Fixture: `hook-notification.jsonl`.
- **Subagents.** Two pairs captured (Explore, general-purpose), `agent_id` shared between `SubagentStart` and `SubagentStop`. Both were `tool_response.status = "async_launched"`: the Agent tool input has no `run_in_background` field on 2.1.309, so a foreground subagent could not be produced by prompting. The CHECKLIST foreground pair is therefore not captured (BLOCKED: not producible on this version). The session also holds one unpaired `SubagentStop` with `agent_type: ""` (an internal agent), which the pairing logic must ignore. `Stop` carries `background_tasks[].id == agent_id`.
- **A subagent's permission prompt.** The subagent's `PreToolUse` and `PermissionRequest` carry `agent_id`; the `Notification` (`permission_prompt`) that follows carries neither `agent_id` nor `agent_type`, so needs-you attributes to the session, not the subagent.
- **Status line stdin** has keys not in FACTS: `session_name` (prompt-derived text; deliberately not decoded), `prompt_cache`, `rate_limits`, `exceeds_200k_tokens`, `fast_mode`, `prompt_id`. Decoder reads only `session_id`, `version`, `model.id`, `cost.total_cost_usd`, `cost.total_duration_ms`.
- Wrapped status line ran live inside Claude Code (`bosun statusline -- sh -c ...` printed `wrapped-ok`); `statusline.json` showed `cost_usd 0.00414144`, matching the fixture.

## Judgment calls

1. **Status line wrapper form.** SPEC shows `exec bosun statusline -- <orig>`. `<orig>` is a shell string, often a pipeline (the owner's is). Unquoted, `exec bosun statusline -- a | b` pipes bosun's output into `b`. `WrapCommand` therefore passes plain commands as words and everything else as `sh -c '<quoted>'`, in both branches. Tested under a real shell, with and without bosun on PATH.
2. **Config walk.** Per-key fallback needs a generic decode (a typed decode aborts at the first mismatch), so the loader decodes to a map and walks it against one key table. Unknown keys are the leaves the table does not know; this is equivalent to `MetaData.Undecoded()` but does not use it. `bosun config` prints warnings as `# warning:` comment lines so the output stays valid TOML.
3. **Hooks use the default key regex.** SPEC says hooks never read config but also that hook and ingest share `task.key_regex`. The hook uses `config.DefaultKeyRegex` (one shared parser, `internal/task`); a customized regex takes effect at ingest. Question for later: should ingest re-derive the task for sessions whose hook-time task came from the default pattern?
4. **Streaming decoder.** The hook walks the top-level JSON object token by token instead of unmarshaling a struct, so a payload cut at the 64 KiB cap still yields early keys (session_id comes first in real payloads). Text fields are skipped, never stored in a variable that outlives the call.
5. **Value filters.** Ids, tool names, agent types and notification types must be identifier-like (`[A-Za-z0-9_.:-]`, length-capped); `source` and `reason` collapse to a fixed set plus `other`. Any free-text value fails these checks.
6. **Event schema.** Keys as in SPEC; the SessionStart extras live under `start` (`cwd`, `transcript`, `repo`, `branch`, `task`, `task_src`, `acct`, `stok`, `entrypoint`). `bg` entries are `{id,type,status,count}`. Lines over 2 KiB drop `start`, then `bg`, and set `truncated`.
7. **Heartbeat.** 128 samples per event, flocked with a 25 ms poll (a hook that cannot get the lock skips its sample). Latency is measured from `Run` entry to just before the heartbeat write, so it excludes process start (about 1 ms measured end to end).
8. **Start token** is the raw field-22 tick count on Linux (not derived from `btime`, which moves when the clock steps) and `sec.usec` of `P_starttime` on darwin. `StartTime` converts for diagnostics. `Alive` rejects non-positive pids.
9. **Identity.** `refresh` re-asks `claude auth status --json` only when `identity.json` is missing or older than 1 hour; `install` always writes it. Only the `email` key is decoded.
10. **`Live` for background rows** is `state` in `working` or `blocked`; `IdleSignal` is false for them (D-11).
11. **Version source.** `VERSION` file embedded in the binary; M9 can add `-X main.version`.
12. **Env reads in the hook.** Event data comes only from `TMUX`, `TMUX_PANE`, `CLAUDE_PID`, `CLAUDE_CODE_ENTRYPOINT`, `BOSUN_TASK` (a test asserts nothing else is requested for `SessionStart`; `CLAUDE_CODE_SESSION_ID` is allowlisted but unused because the payload has `session_id`). Path resolution also reads `HOME`, `BOSUN_*_DIR`, `XDG_*_HOME`, `CLAUDE_CONFIG_DIR`, which are locations, not data.
13. **`scripts/redact.py`** is Python (stdlib only) to keep it short; it is a dev tool, not part of the binary.

## Deferrals

- Detached `refresh` spawn on a stale cache, `live.json`, evaluator, tmux conf (`install` says so and prints no tmux edit yet): M1b.
- `refresh` ingest, `today.json`, `spend_samples`: M2. `today.json` and `pace.json` are not written yet.
- `Focus`, `Notifier`: interfaces only, M1b and M3.
- `doctor`, `uninstall`: M6.
- Hook-side `bosun tag` source: M5 (`start.task_src` is `env` or `branch` only).

## Things worth knowing for M1b

- Pane locator comes from `$TMUX` (`socket,serverpid,session`) plus `$TMUX_PANE`, on every event, so a resumed session rebinds on its next hook.
- `agents.json` rows hold `start_token` for pid rows, so the evaluator can call `Row.Live(procs)` without more `/proc` reads per tick.
- The real user environment has two stale-looking `kind: background` rows (weeks old, `state: blocked`) in `claude agents --json`; `Live` counts `blocked` as live, so they will read as live agents in M1b until Claude Code drops them. Decide there whether stale background rows should count toward the segment.
- Claude Code auto-updates in place while sessions run (see the isolation table).
