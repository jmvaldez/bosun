# FACTS-CHECK: re-verification of kit/FACTS.md

Captured 2026-10-23 on the build machine. Everything ran in `/tmp/bosun-cap`, with settings passed per run via `claude --settings <file>`. tmux ran only on private servers (`tmux -L bosun-cap-<n> -f /dev/null`, n = 1–4), all killed afterwards. No `XDG_*` was set. Captures went through an `env -u CLAUDE* -u TMUX*` wrapper so the capturing agent's own env did not leak in. Model was `--model haiku` (resolves to `claude-haiku-5-5`). Total spend was about $0.042.

## Versions

| Item | Kit captured | This machine |
|---|---|---|
| OS | macOS | Pop!_OS 22.04, kernel 7.1.1-76070101-generic, GNOME Shell 42.9 |
| Claude Code | 2.1.307 | 2.1.309 |
| tmux | 3.5a | 3.2a |
| notify-send / libnotify | n/a | 0.7.9 (`libnotify-bin 0.7.9-3ubuntu5.22.04.1`) |
| Notification server | n/a | gnome-shell, spec 1.2, caps `actions body body-markup icon-static persistence sound` |
| jq | n/a | 1.6 |
| Managed settings | n/a | `/etc/claude-code/` absent |

## ~/.claude/settings.json stat (`stat -c '%Y %s'`)

| When | mtime | size |
|---|---|---|
| Before | 1788389282 | 4210 |
| After | 1788389282 | 4210 |

Unchanged. Side effects outside `/tmp/bosun-cap`, all written by Claude Code itself:
- Transcripts under `~/.claude/projects/-tmp-bosun-cap/`.
- The workspace-trust acceptance written to `~/.claude.json`.

The user's own settings merge into every run: `permissions.defaultMode: "auto"`, plus their own `SessionStart(*)`, `PreToolUse(Bash)` and `PostToolUse(Bash)` hooks. Captures therefore forced `--permission-mode default`.

---

## F1. `--settings` and `--plugin-dir` are per run — UNCHANGED

- `settings.json` stat was identical before and after.
- The plugin loaded inline (`bosuncap@inline` in the `-p` init `plugins[]`), and its SessionStart hook fired.

## F2. Hook events — MOSTLY UNCHANGED, with changes

| Run | Mode | Events (in order) |
|---|---|---|
| A `Read a.txt` | `-p` | SessionStart, UserPromptSubmit, PreToolUse(Read), PostToolUse, PostToolBatch, Stop, SessionEnd |
| C `Read nope.txt` | `-p` | … PreToolUse(Bash), PostToolUseFailure, PostToolBatch, Stop … |
| D `Run mkdir zz via Bash` | `-p`, mode default | SessionStart, UserPromptSubmit, PreToolUse(Bash), **PermissionRequest**, PostToolBatch, Stop, SessionEnd (1 permission denial, no PostToolUse) |
| E Agent tool with an Explore subagent | `-p` | SessionStart, UserPromptSubmit, PreToolUse(Agent), PostToolUse(Agent), SubagentStart, PostToolBatch, **Stop**, [subagent tool events with `agent_id`], SubagentStop, **UserPromptSubmit**, **Stop**, SessionEnd |
| Invalid model | `-p` | SessionStart, UserPromptSubmit, **StopFailure** (`error: "model_not_found"`), SessionEnd. No Stop. |
| Interactive, permission prompt | TUI | PreToolUse and PermissionRequest at +2.06 s; Notification `permission_prompt` at +8.12 s (about 6 s later, no keystroke) |
| Interactive, answer "No" | TUI | **No events for 85 s.** The screen shows `Interrupted · What should Claude do instead?`; `agents --json` reports `idle`. |
| Interactive, `hi` then wait | TUI | UserPromptSubmit, Stop, Notification `idle_prompt` about 60 s after Stop |
| Interactive `/clear` | TUI | SessionEnd `reason=clear` (old id), SessionStart `source=clear` (new id) |
| Interactive `/exit` | TUI | SessionEnd `reason=prompt_input_exit` |

**Unchanged:**
- 33 documented events, and `MessageDisplay` still exists.
- `SubagentStart` and `SubagentStop` share `agent_id` and `agent_type`. `SubagentStop` adds `agent_transcript_path`. `background_tasks[].id` equals the `agent_id`.
- `PermissionRequest` fires immediately, and `permission_prompt` follows about 6 s later.
- `StopFailure` fires on an API error, with no `Stop`.
- Hooks for one event run in parallel.
- `async: true` hooks are killed at `-p` teardown.
- Exec form (`command` plus `args`, no shell) is supported and verified live.
- The `SessionEnd` budget is 1.5 s, raised by a longer per-hook timeout up to 60 s.

**Changed:**
- **`-p` denial:** a denied permission under `-p` now fires `PermissionRequest`, then `PostToolBatch`.
- **Background subagents:**
  - The Agent tool runs subagents in the background by default (`tool_response.status = "async_launched"`).
  - The main `Stop` fires first, with `background_tasks: [{type:"subagent", status:"running", …}]`.
  - When the subagent finishes, Claude Code injects a synthetic `UserPromptSubmit` (new `prompt_id`, same key set as a real prompt), then a second `Stop` with `background_tasks: []`.
  - The transcript marks the synthetic user line with `turnOrigin: "task_notification"` and `promptSource: "system"`. The hook payload has no marker.

**New:**
- An interactive "No" at a permission prompt fires no event at all.

## F3. Hook payload fields — UNCHANGED core, with additions

| Event | Keys |
|---|---|
| SessionStart (`-p`) | cwd, hook_event_name, session_id, source, transcript_path |
| SessionStart (interactive) | + **model**, **scratchpad_dir** |
| UserPromptSubmit | cwd, hook_event_name, permission_mode, prompt, prompt_id, session_id, transcript_path |
| PreToolUse | cwd, effort, hook_event_name, permission_mode, prompt_id, session_id, tool_input, tool_name, tool_use_id, transcript_path (+ agent_id, agent_type in a subagent) |
| PermissionRequest | cwd, effort, hook_event_name, permission_mode, permission_suggestions, prompt_id, session_id, tool_input, tool_name, transcript_path |
| PostToolUse | PreToolUse keys + duration_ms, tool_response |
| PostToolUseFailure | cwd, duration_ms, effort, **error**, hook_event_name, **is_interrupt**, permission_mode, prompt_id, session_id, tool_input, tool_name, tool_use_id, transcript_path |
| PostToolBatch | cwd, effort, hook_event_name, permission_mode, prompt_id, session_id, **tool_calls[]** {tool_input, tool_name, tool_response, tool_use_id} |
| Notification | cwd, hook_event_name, message, notification_type, prompt_id, scratchpad_dir, session_id, transcript_path (no `title` seen) |
| SubagentStart | agent_id, agent_type, cwd, hook_event_name, prompt_id, session_id, transcript_path |
| SubagentStop | agent_id, agent_transcript_path, agent_type, background_tasks, cwd, effort, hook_event_name, last_assistant_message, permission_mode, prompt_id, session_crons, session_id, stop_hook_active, transcript_path |
| Stop | background_tasks, cwd, effort, hook_event_name, last_assistant_message, permission_mode, prompt_id, session_crons, session_id, stop_hook_active, transcript_path |
| StopFailure | cwd, effort, error, hook_event_name, **last_assistant_message**, prompt_id, session_id, transcript_path |
| SessionEnd | cwd, hook_event_name, **prompt_id**, reason, session_id, transcript_path |

Other changes:
- `effort` is an object `{level}`.
- `SessionStart.model` now appears in interactive sessions.
- `StopFailure.error` looked like an enum here.
- New text-bearing paths that the decoder must drop:
  - `PostToolBatch.tool_calls[].tool_input` and `.tool_response`
  - `PostToolUseFailure.error`
  - `StopFailure.last_assistant_message`
  - `PermissionRequest.permission_suggestions[].directories`
  - `scratchpad_dir`

## F4. Hook process environment — UNCHANGED, with a nuance

- **Variable names present:**
  - `TMUX`, `TMUX_PANE`
  - `CLAUDE_PID`, `CLAUDE_CODE_SESSION_ID` (equals the payload `session_id`), `CLAUDE_PROJECT_DIR`, `CLAUDE_EFFORT`
  - `CLAUDECODE`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_CODE_CHILD_SESSION`, `CLAUDE_CODE_SESSION_ATTENDED`
  - `CLAUDE_CODE_MESSAGING_SOCKET`, `CLAUDE_CODE_MESSAGING_TOKEN` (secret-like; not on bosun's allowlist)
  - `CLAUDE_ENV_FILE` (SessionStart only)
- **`CLAUDE_PID`:** in exec form, `PPID == CLAUDE_PID`. In shell form, `CLAUDE_PID` is the grandparent. bosun uses exec form.
- **No controlling terminal**, unchanged.
- **PATH:** the hook PATH includes `~/.local/bin`. Plugin `bin/` and plugin `env` do not reach hooks.

## F5. `claude agents --json` — CHANGED (background rows)

- About 0.17–0.18 s per call.
- Interactive and `-p` row keys: cwd, kind, name, pid, sessionId, startedAt (number), status, plus `waitingFor` when waiting.
- **A `-p` process reports `kind: "interactive"`.**
- **Background rows have no `pid` and no `status`.** Their keys are cwd, id, kind, name, sessionId, startedAt, state.
- New flags: `--all`, `--cwd <path>`.
- Rows vanish within 0.2 s of exit.
- The "row briefly lacks `status`" window was not observed.

## F6. Status line stdin — UNCHANGED

- **Paths:** session_id, transcript_path, cwd, scratchpad_dir, effort.level, model.id, model.display_name, workspace.current_dir, workspace.project_dir, version, output_style.name, cost.total_cost_usd, cost.total_duration_ms, cost.total_api_duration_ms, cost.total_lines_added, cost.total_lines_removed, context_window.*, thinking.enabled.
- **`/clear`:** resets `cost.total_cost_usd` to 0 under the new id.
- **Plugin `statusLine`:** ignored.
- **Not exercised interactively:** resume, fork, `/login`.

## F7. Transcript layout — UNCHANGED core, with new line types

- **Paths:** unchanged, including `<sid>/subagents/agent-<agentId>.jsonl` plus `.meta.json`.
- **Assistant lines:**
  - Unchanged: `message.id`, `requestId`, `message.model`, `timestamp`, and `message.usage` including `cache_creation.{ephemeral_5m_input_tokens,ephemeral_1h_input_tokens}`.
  - New keys on the line: `requestedModel`, `advisorModel`, `requestRef`, `apiBlockIndex`, `effort`, `perTurnEffort`, `thinkingDisplay`, `thinkingDurationMs`, `wireToolInputs`.
  - New usage keys: `fallback_credit`, `inference_geo`, `iterations`, `output_tokens_details`, `server_tool_use`, `service_tier`, `speed`.
- **New user-line keys:** `turnOrigin`, `turnPosition`, `promptSource`, `origin`, `permissionDecision`, `toolDenialKind`, `sandboxed`.
- **New line types:**
  - `api-request` (no usage or cost; `querySource` values `repl_main_thread`, `sdk`, `agent:builtin:Explore`)
  - `api-request-blob`, `api-request-shape`, `atis-latch`, `mode`, `permission-mode`, `ai-title`, `last-prompt`, `queue-operation`
- **Line sizes:** up to about 124 KB (`attachment`). Even a one-word session's transcript is about 200 KB.
- **`cost-state`:**
  - Unchanged keys plus `hasUnknownModelCost`, `totalAPIDuration*`, `totalDuration`, `totalLines*`, `totalToolDuration`.
  - Written at `/clear` and at exit. `/exit` can write two identical lines.
- **Snake-case `session_id`:** equals the launch id and survives `/clear` interactively, but is **absent from `-p` transcripts**.
- **Not re-verified:** line latency, `cleanupPeriodDays`, `CLAUDE_CONFIG_DIR`.

## F8. Transcripts undercount — NOT REPRODUCED at this scale

- **Small sessions:** deduped transcript tokens exactly matched `cost-state.modelUsage` for three `-p` sessions and one interactive session, including a small subagent run.
- **Side requests:** the interactive session showed a small gap, consistent with side requests such as `ai-title`.
- **Still open:** re-check on a real subagent-heavy session.

## F9. Forks, `/clear`, resume — UNCHANGED

| Run | session_id | total_cost_usd | source | startTime |
|---|---|---|---|---|
| new | e7fadb76… | 0.00203312 | startup | 1792795060423 |
| resume | same | 0.0042531 | resume | same |
| fork | 596060ec… | 0.00454509 (includes parent) | fork | equal |

- **Fork copies:** the fork shares all parent `uuid`s, and copied lines keep `uuid`, `message.id`, `requestId` and `timestamp`. **`sessionId` is rewritten to the fork id**, so the copies are not byte-verbatim.
- **No `forkedFrom` field** exists.
- **Not tested:** `/login`.

## F10. Plugins — UNCHANGED

- **Double-fire:** the same handler in the plugin and in settings fires twice.
- **Plugin settings:** the plugin's `env` and `statusLine` are dropped.
- **Plugin `bin/`:** not on the hook PATH.
- **`/hooks` labels:** `/hooks` labels plugin hooks. Hooks passed via `--settings` fire but **do not appear in `/hooks`**.
- **Not exercised:** marketplace install, managed-policy keys.

## F11. `subagentStatusLine` — NOT RE-VERIFIED (not consumed by the spec)

## F12. Cost semantics — UNCHANGED

- **Interactive:** `/cost` showed $0.0042, and the status line `cost.total_cost_usd` showed 0.00419531.
- **`-p`:** `total_cost_usd` equals `cost-state.totalCostUSD`.
- **Plan:** this is a Claude Pro plan, and the figures are API-rate estimates.

## F13. tmux 3.2a — MOSTLY UNCHANGED; `-T` unavailable

- **`display-popup`:**
  - Exists and accepts `-CE -c -d -h -t -w -x -y`.
  - **`-T` is rejected, and an unguarded `bind … display-popup -T` makes `source-file -q` fail (rc=1).**
  - Inside the popup, `TMUX_PANE` is empty, while `display -p '#{client_name}'` and `list-clients` work.
- **Version gate:** `#{>=:#{version},3.3}` gives 0 and `#{>=:#{version},3.2}` gives 1. An `if -F` gate sourced cleanly twice. The comparison is a string compare, so a hypothetical "3.10" compares below "3.3".
- **Client formats:** `client_termfeatures` includes `focus`. With `focus-events on`, `client_flags` shows `focused` after `ESC [ I` and drops it after `ESC [ O`.
- **Targeting:**
  - `switch-client -c <client> -t %pane` works across sessions.
  - `set -w -t %pane @opt` lands on that pane's window.
- **Format expansion:**
  - `set -gF` runs `#()` at set time (the stored value was `<'echo job' not ready>`).
  - Plain `set -g` stores the format literally.
  - `run` expands formats.
- **Idempotency:**
  - The `#{m:*bosun status*,#{status-right}}` guard is idempotent.
  - **`set-hook -ag client-attached` duplicates on every re-source.**
- **`tmux info`:** returns "no current client", so it is still useless as a probe. `has-session` works.

## F14. Linux platform — CHANGED for this distro

- **notify-send:** 0.7.9 has **no `--print-id`, no `--replace-id`, no `--action`, no `--wait`**.
- **gdbus:** `org.freedesktop.Notifications.Notify` returns `(uint32 <id>,)`, and `CloseNotification <id>` works. `replaces_id` was not validly tested.
- **`/proc/<pid>/stat` field 22:** `btime + f22/CLK_TCK` equals `ps -o lstart`. Parse after the **last** `)`; naive splitting breaks on a comm like `a b) c`.

---

## Spec-affecting changes

1. **Linux notifier (§6).**
   - Problem: 22.04's `notify-send` cannot replace or retract.
   - Proposed: primary Linux backend is direct D-Bus via `gdbus call … Notifications.Notify`, which returns an id and supports `replaces_id`, `CloseNotification` and actions. `notify-send --print-id` stays as a fallback on ≥ 0.8. Parse the id with a regex on `uint32 (\d+)`.
2. **needs-you stuck after an interactive "No" (§5).**
   - Problem: no hook fires after the "No".
   - Proposed: the evaluator drops needs-you to `idle` when `agents.json` shows the live session with `status != waiting` for at least one refresh after the needs-you entry. This is a narrow exception to "hooks win".
3. **Background subagents (§5, popup).**
   - Problem: a synthetic `UserPromptSubmit` follows each background subagent completion.
   - Proposed:
     - Prompt numbering comes from transcript user lines, excluding `turnOrigin == "task_notification"`.
     - The turn start (edit count, done-notice duration) carries forward when the previous `Stop` had running background tasks.
4. **`set-hook -ag client-attached` not idempotent (§7).**
   - Proposed: use a fixed index (`set-hook -g 'client-attached[42]' …`), verified on 3.2a in M1b.
5. **`-p` denial fires `PermissionRequest` (§5/§6).**
   - Problem: a `-p` denial produces a needs-you edge that clears in milliseconds.
   - Proposed:
     - Never notify needs-you for non-interactive sessions.
     - Add `CLAUDE_CODE_ENTRYPOINT` to the hook env allowlist, after capturing its `-p` value.
     - Keep the dispatcher re-check.
6. **`agents --json` background rows lack `pid` and `status` (§3/§5).**
   - Proposed:
     - Background-row liveness keys on `sessionId` and `state`.
     - The `idle`-over-stale-`working` rule does not apply to background rows.
     - Never use `kind` to detect `-p`.
7. **tmux 3.2a (D-3 confirmed).** Gate `-T` with `if -F '#{>=:#{version},3.3}'`. `doctor` treats 3.2 as popup yes, title no. The Linux focus probe works on 3.2a.
8. **Decoder drop list (F3).** Add the new text paths to `TestPromptFilter` sentinels.
9. **Ingest (§4).**
   - Scanner max line 1 MiB or more.
   - Skip the new non-message line types.
   - `launch_session_id` falls back to `sessionId` for `-p`.
   - Fork detection by `uuid` stays correct.

## BLOCKED for human (gaps, not blockers)

- **Esc or Ctrl+C at a permission prompt** (does any event fire?). Use a private `tmux -L bosun-cap-9` server, send `Run mkdir zz via Bash`, then `Escape`, and check `jq -c .hook_event_name /tmp/bosun-cap/hooks.jsonl`.
- **gdbus `replaces_id`.** Notify with id 0, re-notify with the returned id (expect the same id back), then CloseNotification.
- **`CLAUDE_CODE_ENTRYPOINT` value in the hook env**, for `-p` vs interactive.
- **Fixed-index `set-hook` idempotency on 3.2a.** This is agent-verifiable in M1b.
- **F8 at realistic scale:** run `/tmp/bosun-cap/f8.sh <sessionId>` against a real subagent-heavy session (read-only).
- **Not re-verified:**
  - F7: latency, `cleanupPeriodDays`, `CLAUDE_CONFIG_DIR`.
  - F9: `/login`.
  - F10: marketplace and managed policy.
  - F11.
  - F14: macOS and WSL items (D-1).
