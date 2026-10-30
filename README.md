# bosun

**A tmux status bar and dashboard for every Claude Code session you have running.**

For people who live in tmux and run several Claude Code sessions in parallel.

With that many sessions you lose track: one is stuck at a permission prompt,
another finished ten minutes ago, a subagent has been looping for twenty
minutes. bosun shows all of that at a glance and takes you straight to the pane
that needs you.

```
⚑ 2 waiting  ▲ 1 runaway  ✓ 1 done  $23.40 / $50
```

- **Know who's waiting.** The bar counts sessions blocked on a prompt or done
  with their turn, usually within a second. `prefix + a` jumps to the oldest.
- **Catch runaways early.** Alerts fire for long or expensive subagents, cost
  steps, stalled sessions, and days running above your usual spend.
- **See what each one is doing.** `prefix + W` opens a popup with each
  session's task, repo, spend, and live activity (`running go test ./...`,
  `editing ui/view.go`, `waiting on 2 subagents`).
- **Know where the tokens went.** `bosun spend` breaks cost down by day, task,
  session, prompt and request, as estimates at API list prices. On a Pro or Max
  plan, read them as relative spend.
- **Get pinged, not interrupted.** Desktop notices when a session needs you or
  finishes, except for the pane you're already looking at.

Local only: no daemon, no telemetry, no network calls from bosun itself.

> **Alpha (0.1.0).** Tested on Linux (Pop!_OS 22.04, tmux 3.2a, Claude Code
> 2.1.309+). macOS is partly tested. WSL is not supported.

## Build from source

Requires Go 1.26+, tmux 3.2+, and Claude Code with plugin support.

```sh
git clone https://github.com/jmvaldez/bosun && cd bosun
make install      # installs to ~/.local/bin/bosun and the plugin to ~/.local/share/bosun; set PREFIX to change
bosun setup       # installs the plugin, loads the tmux config, runs checks
```

Restart Claude Code and send one prompt, then run `bosun doctor` to confirm.
`bosun setup` never edits your tmux config. See
[docs/configuration.md](docs/configuration.md#what-bosun-setup-does) for exactly
what it changes.

To remove it: `bosun uninstall --apply` (add `--purge` to delete its data).

## Roadmap

The next milestone is a published alpha package (Homebrew, `.deb`, `.rpm`).
See the [Roadmap](https://github.com/jmvaldez/bosun/wiki/Roadmap) for details.

## Technical notes

**Keys:** `prefix + W` popup · `prefix + $` spend view · `prefix + a` next
waiting session. In the popup, `enter` jumps, `!` interrupts, `d` diffs, `q` closes.

**CLI:**

```
bosun status [--json]       current state of every session
bosun spend [--by K]        cost by day, task, repo, session, prompt, request, model, subagent
bosun tag TEXT              label this session with a task (e.g. ABC-123)
bosun run --task TEXT       open a new Claude Code window with a task set
bosun doctor                self-check; run this first if the bar is empty
```

Run `bosun help` for the full list.

**How it works:** a Claude Code plugin registers hooks that append events to a
state file in a few milliseconds. tmux runs `bosun status --tmux` each second
to evaluate state and alerts. Transcripts are ingested into SQLite for the
spend breakdown. Details: [docs/architecture.md](docs/architecture.md).

**Cost figures** are estimates at API list prices, whatever your plan, so on a
subscription they show relative spend, not your bill. Live session cost needs
bosun to wrap your `statusLine.command`. `bosun setup` asks before it does that.

**Privacy:** prompt text is never stored or logged. The popup's activity line
is read from the transcript at render time. Set `privacy.show_text = false` to
hide it.

**Config:** `~/.config/bosun/config.toml`. Every key is optional and changes
apply live. Reference: [docs/configuration.md](docs/configuration.md).

**Known limits:**

- `bosun` must be on the PATH that Claude Code hooks inherit. Hooks fail
  silently, so a broken install just shows an empty bar.
- Only sessions inside tmux panes are tracked.
- Pressing Escape at a permission prompt fires no hook, so needs-you takes
  6–10 s to clear.
- On macOS, click-through and focus detection aren't built yet.

**Development:** `make test`, `make check` (vet, staticcheck, cross-compile).
`scripts/demo.sh up` starts a sandboxed tmux server that doesn't touch your
real setup. `CONTEXT.md` defines terms.

## License

MIT. See [LICENSE](LICENSE).
