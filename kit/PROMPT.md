# Kickoff prompt

Copy the kit into `<repo path>/kit/`, start a fresh Claude Code session in `<repo path>`, and paste everything below the line. Fill in the bracketed values first.

---

You are building bosun from scratch: a single Go binary that watches Claude Code agent sessions across tmux, tells me when one needs me, flags runaways and hot spending days, and tracks spend per session. It is a personal tool. macOS first, with Linux and WSL seams.

## Fill in before starting

| Placeholder | My value |
|---|---|
| Repo path | `<repo path>` |
| Go module path | `<module path, for example github.com/<you>/bosun>` |
| How I start Claude Code (`<launcher>`) | `<launcher, for example claude or an alias/function>` |
| tmux prefix key | `<prefix>` |
| Keys free after prefix for popup and jump | `<popup key, default W>`, `<jump key, default a>` |
| Terminal app and its macOS bundle id | `<terminal>`, `<bundle id>` |
| Theme or plugin manager in my tmux conf | `<none / tpm / other>` |
| Status line command today | `<current statusLine command, or none>` |
| Day boundary for spend | `<local or utc>` |
| Issue key pattern if not the default | `<regex or default>` |
| Platforms I will test on | `<macOS / Linux / WSL>` |

## Read first

All in `kit/`, in this order: `SPEC.md` (what to build, milestones, acceptance), `FACTS.md` (verified Claude Code behavior), `LESSONS.md` (every bug the last build hit), `CONTEXT.md` (terms), `CHECKLIST.md` (what I verify by hand). SPEC wins over everything else. Copy `kit/CONTEXT.md` to the repo root. Never edit `kit/`.

## Hard constraints

- No daemon. Hooks and the status line finish under 50 ms, never block, always exit 0.
- Never store, log, or notify prompt text. Decoders are allowlists.
- Never edit my `~/.claude/settings.json`, my tmux conf, my shell rc files, or my status line script. Print the edits; I make them.
- Never set `XDG_*` for any process that runs `claude`. bosun uses `BOSUN_STATE_DIR`, `BOSUN_DATA_DIR`, `BOSUN_CONFIG_DIR`, `BOSUN_CACHE_DIR` for isolation.
- Never run against my real tmux server. Use private servers: `tmux -L bosun-test-<n> -f /dev/null`. Check a server with `has-session`.
- Every exec has a context timeout. Every lock is non-blocking or deadline-bound.
- Tests that read real transcripts, drive real tmux, or hit the network are opt-in via `BOSUN_TEST_REAL_SESSIONS`, `BOSUN_TEST_TMUX`, `BOSUN_TEST_NETWORK`.
- `CGO_ENABLED=0` everywhere.
- No commits, pushes, tags, or releases until I ask.

## How to work

1. Re-verify FACTS.md on my installed versions first. Write `build-log/FACTS-CHECK.md`. Stop and tell me about any fact that changed in a way that affects the spec.
2. Build milestones strictly in order: M1a, M1b, M2, M3, M4, M5, M6, M7, M8, M9. One milestone at a time, one writer on the tree.
3. Each milestone: build, `go vet`, `go test ./...`, cross-build all four targets, then write `build-log/<M>.md` with files, an acceptance table, judgment calls, and deferrals.
4. Acceptance rows are `PASS` only with evidence (test name, command output, capture). Anything that needs me is `BLOCKED` with the exact commands for me to run. "PASS by design" is not allowed.
5. Before writing each milestone's code, reread the LESSONS entries its acceptance checks cite.
6. Live tests against Claude Code use per-run flags only: `claude --settings '<json>'` and `claude --plugin-dir ./plugin`. Never install the plugin or change my settings yourself.
7. tmux rendering is verified by capturing a real client: run the test server, attach a client to it from a pane in a second private server, then `capture-pane -p` that pane. Format evaluation alone does not count.
8. Batch questions for me at milestone boundaries, each with your assumed default, and keep going on the default unless it blocks.
9. Comments explain why, never what. TODOs reference an issue.

## After M9

1. Review pass. Review the whole tree against SPEC.md and LESSONS.md along several angles (privacy, hook contract, process and locking, spend math, tmux, setup safety, concurrency, test isolation). Send every candidate finding to an independent verifier that did not find it; keep only confirmed ones. Fix them, add a regression test for each, and write `build-log/REVIEW.md`.
2. Demo script `scripts/demo.sh` with `up`, `agent [TASK] [-- claude args]`, `attach`, `down`, `reset`. It runs a private tmux server, sources the generated conf, uses `BOSUN_*_DIR` temp dirs, loads the plugin with `--plugin-dir` and the status line with `--settings`, and never sets `XDG_*`. `up` records `readlink ~/.local/bin/claude`; `down` asserts it is unchanged.
3. Write `build-log/HANDOFF.md`: how to run the demo, the CHECKLIST items still open, known limits, and the real install steps for me to run by hand.

## Distribution target

`make install` (temp file then `mv` into `~/.local/bin/bosun`), plugin installed from the repo as its own marketplace (`claude plugin marketplace add <repo path>`, `claude plugin install bosun@bosun`), `make release` tarballs for GitHub releases, and an optional formula for a personal Homebrew tap.

Start with step 1.
