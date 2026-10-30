# Verified Claude Code facts

Captured October 2026. Unless noted: Claude Code 2.1.307 on macOS, tmux 3.5a, Go 1.26.
Claude Code changes fast. Re-verify first: before M1a, run each capture command below on your installed version and note any difference in `build-log/FACTS-CHECK.md`. Where a fact changed, the capture wins over this file.

Capture setup used throughout: a throwaway settings file passed per run, so your real settings are never touched.

```sh
mkdir -p /tmp/bosun-cap && cd /tmp/bosun-cap
claude --version
```

## F1. `--settings` and `--plugin-dir` are per run

- `claude --settings '<json or path>'` applies for that run only. It does not write or merge into `~/.claude/settings.json`. Verified 2.1.307 (settings file mtime unchanged).
- `claude --plugin-dir <path>` loads a plugin for that run only, without a marketplace install.
- Re-verify: note `stat` of `~/.claude/settings.json`, run a session with both flags, exit, compare.

## F2. Hook events

- 33 documented events. bosun uses 13 (SPEC section 3).
- `SubagentStart` and `SubagentStop` exist and carry `agent_id` and `agent_type`. `SubagentStop` adds `agent_transcript_path`. It also fires for internal agents (prompt suggestions) with no matching start; ignore unpaired stops.
- `PermissionRequest` fires immediately. Notification `permission_prompt` follows about 6 s later with no keystroke. `idle_prompt` fires about 60 s after a turn ends. Other blocking types: `elicitation_dialog`, `elicitation_url_dialog`, `agent_needs_input`.
- `Stop` ends a turn; `StopFailure` on API error; a user interrupt (Ctrl+C) fires neither.
- A denied permission under `-p` produced only `PostToolBatch` (no `PostToolUse`).
- `MessageDisplay` fires per streamed text chunk and carries assistant text. Never register it.
- Hooks for one event run in parallel. Default timeout 600 s; `SessionEnd` has a 1.5 s budget.
- `async: true` hooks are killed at `-p` teardown, so bosun uses sync hooks.
- Re-verify: a settings file registering `cat >> /tmp/bosun-cap/hooks.jsonl` on each event, in matcher-group form:

```sh
claude --settings '{"hooks":{"SubagentStart":[{"hooks":[{"type":"command","command":"cat >> /tmp/bosun-cap/hooks.jsonl"}]}],"SubagentStop":[{"hooks":[{"type":"command","command":"cat >> /tmp/bosun-cap/hooks.jsonl"}]}]}}'
```

Repeat the shape for every event you need. Notification types need an interactive session.

## F3. Hook payload fields

- Every event: `session_id`, `transcript_path`, `cwd`, `hook_event_name`.
- `prompt_id` after the first user input. `permission_mode` on most tool and turn events.
- `agent_id` only inside a subagent. `agent_type` also appears on main-agent events when an `agent` setting or `--agent` is active, so key on `agent_id`.
- No model in payloads (an optional `model` on `SessionStart` was absent in every capture).
- `SessionStart.source`: `startup`, `resume`, `clear`, `compact`, `fork`.
- `SessionEnd.reason`: `clear`, `resume`, `logout`, `prompt_input_exit`, `other`.
- `Stop` and `SubagentStop`: `background_tasks[]` (`type`, `status`, plus text fields), `session_crons`, `last_assistant_message` (text).
- Text-bearing fields to drop: `UserPromptSubmit.prompt`, `tool_input`, `tool_response`, `last_assistant_message`, Notification `message` and `title`, `StopFailure.error`, `background_tasks[].description`, `.command`.
- Re-verify: capture as in F2 and list keys with `jq -c 'keys' /tmp/bosun-cap/hooks.jsonl | sort -u`.

## F4. Hook process environment

- Inherits the Claude process env, including `TMUX` and `TMUX_PANE`, and anything your launcher injects (secrets included).
- Adds `CLAUDE_PID` (equals the hook's parent pid), `CLAUDE_CODE_SESSION_ID`, `CLAUDE_PROJECT_DIR`.
- No controlling terminal.
- Exec-form hooks (`command` plus `args`) run without a shell, so `bosun` must be on the PATH Claude inherited.
- Re-verify: a hook command `env | grep -E '^(TMUX|CLAUDE)' > /tmp/bosun-cap/env.txt`.

## F5. `claude agents --json`

- Documented as the supported way for status bars to read session state.
- Lists every live process (interactive, `-p`, background): `pid`, `status` (`busy`, `waiting`, `idle`), `waitingFor` (`permission prompt`, `input needed`, `sandbox request`, `worker request`, `dialog open`), `sessionId`, `cwd`, `kind`, `startedAt`, `name`.
- `name` is derived from prompt text. Never store it.
- Background rows add `id` and `state` (`working`, `blocked`, `done`, `failed`, `stopped`).
- Subagents are not listed. Rows vanish within about 1 s of exit. No pane, model, cost, or branch.
- A brand new row may lack `status` for under a second; treat as busy.
- Costs about 0.4 s wall per call, so cache it.
- Re-verify: `time claude agents --json | jq '.[0] | keys'`.

## F6. Status line stdin

- JSON with `session_id`, `transcript_path`, `model.id`, `version`, `cost.total_cost_usd`, `cost.total_duration_ms`, `cost.total_api_duration_ms`.
- `cost.total_cost_usd` is the whole session total, including subagents and side requests that never reach transcripts. It is the same figure `/cost` shows.
- Resets to $0 on `/clear` (new session id). Restored on resume and on fork. Resets mid-session on `/login` (same id).
- Debounced about 300 ms. Does not run under `claude -p`.
- Plugins cannot set `statusLine`.
- Re-verify (interactive only):

```sh
claude --settings '{"statusLine":{"type":"command","command":"tee /tmp/bosun-cap/sl.json"}}'
```

## F7. Transcript layout

- `<claude dir>/projects/<cwd with non-alphanumerics as ->/<sessionId>.jsonl`.
- Subagents: `<sessionId>/subagents/agent-<agentId>.jsonl` plus `.meta.json`. Every subagent line has `isSidechain: true` and the parent's `sessionId`.
- Skip `*.orphaned-*` and `*.superseded-*` (set-aside copies; parsing them double-counts).
- Assistant line per content block: `message.id`, `requestId`, `message.model`, `timestamp`, `message.usage` (`input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_creation.ephemeral_5m_input_tokens`, `ephemeral_1h_input_tokens`).
- Input and cache fields are final on the first line; only `output_tokens` grows. Keep the max.
- `promptId` is on user lines only.
- `cost-state` line: `totalCostUSD`, `startTime`, `modelUsage`, written on normal exit. Each writer process keeps its own `startTime`.
- No user identity and no per-request cost.
- Lines land about 110 ms after their timestamp.
- Retention: `cleanupPeriodDays`, default 30.
- `CLAUDE_CONFIG_DIR` moves the whole tree.
- Re-verify: `ls <claude dir>/projects/*/ | head` and `jq -c 'select(.type=="cost-state") | keys' <some transcript>.jsonl`.

## F8. Why transcripts undercount

- Side requests (summaries, prompt suggestions, away summaries, compaction, web fetch handling, SDK calls) never appear as assistant lines but are in `cost.total_cost_usd`.
- Many subagent requests never get their final usage line: only a stream-start snapshot with a few output tokens lands. Observed rates ranged from about 30% to over 60% of subagent requests.
- Even main-thread `output_tokens` can fall short of the final value.
- Net: transcript-only spend ran about 0.7x to 0.85x of the true total on typical sessions, lower on subagent-heavy ones.
- Re-verify: compare one ended session's `cost-state.totalCostUSD` with the priced row sum.

## F9. Forks, `/clear`, resume, `/login`

| Transition | Session id | Total | Transcript |
|---|---|---|---|
| `/clear` | new | starts at $0 | new file, nothing copied; `SessionStart.source=clear` |
| resume (`--resume`, `--continue`) | same | restored, keeps growing, same `startTime` | appended |
| fork (`--fork-session`, `/branch`) | new | includes the parent's total at fork time | parent lines copied verbatim (same `uuid`, `message.id`, `requestId`, timestamps); `SessionStart.source=fork` |
| `/login` mid-session | same | resets to $0, new `startTime` | same file |

- No `forkedFrom` field exists. Detect forks by shared `uuid`s, equal first `startTime`, or the hook `source`.
- Transcript lines carry a snake_case `session_id` equal to the launch id, stable across `/clear` in one process (use as `launch_session_id`).
- Re-verify: three `claude -p` runs (new, `--resume`, `--resume --fork-session`) with a one-word prompt, then compare `session_id`, totals, and the first lines of each file.

## F10. Plugins

- A plugin can ship `hooks/hooks.json`. Plugin hooks merge with settings hooks and run in parallel.
- Identical handlers in plugin and settings do not dedupe: registering both double-fires.
- Plugin settings honor only `agent` and `subagentStatusLine`. Everything else, including `statusLine` and `env`, is dropped.
- Plugin `bin/` goes on the Bash tool's PATH only, not on the hook PATH or tmux PATH.
- A repo with `.claude-plugin/marketplace.json` at its root works as a local marketplace: `claude plugin marketplace add <path>`, then `claude plugin install <plugin>@<marketplace>`.
- `enabledPlugins` in settings may be an object (`{"bosun@bosun": true}`).
- Managed settings `allowManagedHooksOnly` or `disableAllHooks` blind bosun. Managed paths: `/Library/Application Support/ClaudeCode/` (macOS), `/etc/claude-code/` (Linux).
- `/hooks` labels each hook with its source plugin.
- Re-verify: `claude --plugin-dir ./plugin` then run `/hooks`.

## F11. `subagentStatusLine`

- Stdin has `tasks[]` keyed by `agent_id` with `tokenCount`, no cost. `tokenCount` cannot be priced.
- Runs only in the interactive TUI while subagent rows show. Not a cost source.

## F12. Cost semantics

- Claude Code computes cost locally at list price unless managed `modelPricing` is set (seen in 2.1.309). The same number feeds `/cost`, the status line, `cost-state`, and OTel.
- Subscription plans (Pro, Max) still get these API-rate estimates. bosun labels all figures as API-rate estimates.
- Re-verify: `/cost` in an interactive session vs the status line capture from F6.

## F13. tmux facts (3.5a)

- `display-popup -E` closes when the command exits. Inside it, `TMUX_PANE` is wrong; use `#{client_name}` and `list-clients`.
- `switch-client -c <client> -t %<pane>` changes session, window, and pane in one call.
- `set -F` expands formats and runs `#()` jobs at set time; `##` escapes.
- `run` and `run-shell` expand formats in their command.
- `#()` jobs are async and rerun on `status-interval`.
- `list-clients -F '#{client_flags} #{pane_id} #{client_termname} #{client_termfeatures}'`: `focused` in flags with `focus-events on`.
- `tmux info` does not exist; use `has-session`.
- Window user options (`set -w -t %<pane> @opt v`) resolve a pane target to its window.
- Pane ids reset when a session manager restores panes.
- Re-verify: `tmux -V`, then the commands above on a private `tmux -L bosun-cap -f /dev/null` server.

## F14. Platform facts

- macOS: `lsappinfo front` gives the frontmost app (about 20 ms). `osascript` notifications may be invisible. `terminal-notifier` needs a one-time permission grant.
- `unix.SysctlKinfoProc` costs about 0.1 ms per pid; `ps` about 50 ms.
- WSL: WSLg does not bridge `notify-send` to Windows toasts. Long-lived tmux servers keep a stale `WSL_INTEROP` socket. `flock` and rename are unreliable on `/mnt/*`.
- Linux: libnotify before 0.8 lacks `--print-id`.
