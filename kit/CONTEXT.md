# bosun glossary

**Agent session**: one Claude Code conversation, identified by its session id. Spend, prompts, and attention state belong to it. Avoid: session (ambiguous), agent, conversation.

**tmux session**: a tmux session, always written in full. One tmux session can hold several agent sessions.

**Subagent**: a child agent an agent session starts. Its spend rolls up to the parent. Avoid: task, child session.

**Task**: a unit of work the user owns, usually an issue key such as `ABC-123`. Spend groups by task above the agent session. Sources in precedence order: `bosun tag`, `BOSUN_TASK` (set by `bosun run`), a key parsed from the start branch, the transcript `gitBranch` (backfill only).

**Issue key**: a string matching `task.key_regex` (default `[A-Z][A-Z0-9]+-\d+`), uppercased.

**Untasked**: an agent session with no task from any source.

**Attention state**: what an agent session needs from the user now: `needs-you`, `working`, `idle`, or `done`. Subagent states are `running` and `ended`. Avoid: status.

**needs-you**: blocked on the human (permission prompt, elicitation dialog, agent input).

**done**: the turn finished with no background work pending. Becomes `idle` once the user looks at the pane.

**Runaway**: a subagent or agent session with an active runaway alert. Outranks `working` and `idle` in display, but is not a fifth state.

**Alert**: a notice raised once per rule and scope, with a short id. Re-arms when its condition clears.

**Dismissed**: an active alert the user hid with `x` or `bosun ack`. Hidden from every surface until its condition clears, then re-armed as usual.

**Catch-up alert**: an alert found by an evaluation run while the user was already at the terminal. Shown in the segment and popup only, never as a desktop notification.

**Peek**: the popup's one-line view of what an agent is doing or asking, read from the transcript tail at render time and never stored.

**Quiet**: a `working` session with no activity between tool calls. A long single tool call is not quiet.

**Pace**: today's spend so far divided by the typical cumulative spend at the same clock time.

**Baseline day**: a past day with spend greater than zero, inside the pace window, excluding today.

**Account key**: the local identity (lowercased login email or account uuid) on every spend row. Never leaves the machine.

**Session total**: the trusted dollar figure for one agent session, matching what Claude Code reports (status line live, `cost-state` at exit).

**Own spend**: a session total minus fork inheritance plus banked spend. Computed in one place.

**Estimate**: a dollar figure bosun computes from transcripts. Always marked `est.`.

**API-rate estimate**: every dollar figure bosun shows, priced at list API rates whatever the plan.

**Unattributed**: the part of a session total the transcript breakdown cannot assign. Computed at query time. Can be negative.

**Fork**: a session started from another's history. New id, inherits the parent's total, copies its requests verbatim.

**Banked spend**: the maximum total kept when a session's reported total resets mid-session.

**Ledger**: the SQLite database `bosun.db`. Only `bosun` CLI processes open it.

**Ingest**: reading transcripts into the ledger. One idempotent path for live and backfill.

**Pane locator**: (tmux socket, server pid, pane id) for where an agent session runs. An attribute, not a key.

**Pane gone**: a locator whose server pid changed or whose pane id no longer exists.

**Focused pane**: the pane shown in a tmux client that has focus, with its terminal frontmost (macOS).

**Waiting queue**: what `prefix + a` walks: `needs-you` oldest first by time in state, then `done`.

**Notification**: a desktop notice from `bosun notify-dispatch`. Distinct from Claude Code's `Notification` hook event.

**Hook heartbeat**: last hook time, plugin version, and latency samples, used by `doctor`.

**Wrapped status line**: the user's `statusLine` command after they edit it to call `bosun statusline` before their original command.

**bosun plugin**: the Claude Code plugin that ships bosun's hooks.

**Start token**: an opaque process start marker that stops a reused pid from looking alive.

**Launcher**: the command the user types to start Claude Code (plain `claude`, an alias, or a wrapper function). `bosun run` uses it through `$SHELL -ic`.

**Context fill**: the share of the context window the session's last request used, read from the status line. Unknown until the first response, and shown as `—`, never as 0.
