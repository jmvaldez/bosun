#!/usr/bin/env bash
# Drive bosun on this machine without touching your tmux, your Claude settings,
# or your real bosun dirs.
#
#   scripts/demo.sh up                      start a private tmux server with the bar
#   scripts/demo.sh agent [TASK] [-- ARGS]  open a Claude Code window (hooks and status line wired)
#   scripts/demo.sh attach                  look at it (nested tmux is fine)
#   scripts/demo.sh down                    stop it and check nothing outside the demo changed
#   scripts/demo.sh reset                   down, then delete the demo's state
#
# Extras: `bosun ARGS...` runs the built bosun with the demo's dirs; `env` prints
# them for `eval`.
#
# Knobs (set before `up`): DEMO_NOTIFY (auto|off, default auto), DEMO_SESSION_COST
# (USD step, e.g. 0.05, to trip a runaway quickly), DEMO_THEMEPACK=0 to skip
# sourcing your tmux-themepack theme.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO="${BOSUN_DEMO_DIR:-${TMPDIR:-/tmp}/bosun-demo-$(id -un)}"
SOCK="bosun-demo"
SESSION="demo"
BIN="$REPO/bin/bosun"
THEME="$HOME/.tmux/plugins/tmux-themepack/powerline/block/blue.tmuxtheme"
CLAUDE_LINK="$HOME/.local/bin/claude"

# Everything runs with the demo's dirs and without XDG overrides (a global
# XDG_DATA_HOME once made Claude Code's updater install into a temp dir) and
# without the variables that tie a child to a parent Claude Code session.
demo_env() {
  env -u XDG_DATA_HOME -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u XDG_CACHE_HOME \
    -u CLAUDECODE -u CLAUDE_CODE_ENTRYPOINT -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_EXECPATH \
    -u CLAUDE_CODE_MESSAGING_SOCKET -u CLAUDE_CODE_MESSAGING_TOKEN -u CLAUDE_CODE_CHILD_SESSION \
    -u CLAUDE_CODE_SESSION_ATTENDED -u CLAUDE_PID -u CLAUDE_EFFORT -u TMUX -u TMUX_PANE \
    PATH="$REPO/bin:$PATH" \
    BOSUN_STATE_DIR="$DEMO/state" BOSUN_CACHE_DIR="$DEMO/cache" \
    BOSUN_DATA_DIR="$DEMO/data" BOSUN_CONFIG_DIR="$DEMO/config" \
    "$@"
}

tm() { demo_env tmux -L "$SOCK" -f /dev/null "$@"; }
running() { tm has-session -t "$SESSION" 2>/dev/null; }
die() { echo "demo: $*" >&2; exit 1; }

real_bosun_dirs() {
  local d
  for d in "$HOME/.local/state/bosun" "$HOME/.local/share/bosun" "$HOME/.config/bosun" "$HOME/.cache/bosun"; do
    if [ -e "$d" ]; then echo "$d present"; else echo "$d absent"; fi
  done
}

cmd_up() {
  command -v tmux >/dev/null || die "tmux not found"
  command -v claude >/dev/null || die "claude not found on PATH"
  if running; then die "already up (use attach, or reset)"; fi
  # Always rebuild: a stale binary would generate a stale tmux conf.
  (cd "$REPO" && make build >/dev/null)

  mkdir -p "$DEMO"/{state,cache,data,config,project}
  # Within one up/down cycle the claude symlink must not move. (Claude Code
  # updates itself between cycles; that is not this script's doing.)
  readlink "$CLAUDE_LINK" >"$DEMO/claude-link.before" 2>/dev/null || echo "(not a symlink)" >"$DEMO/claude-link.before"
  real_bosun_dirs >"$DEMO/real-dirs.before"
  stat -c '%Y %s' "$HOME/.claude/settings.json" >"$DEMO/settings-stat.before" 2>/dev/null || echo none >"$DEMO/settings-stat.before"

  {
    echo "[notify]"
    echo "backend = \"${DEMO_NOTIFY:-auto}\""
    if [ -n "${DEMO_SESSION_COST:-}" ]; then
      echo "[rules]"
      echo "session_cost = $DEMO_SESSION_COST"
    fi
  } >"$DEMO/config/config.toml"

  # A real wrapped status line, as `bosun install` prints it, around a trivial command.
  cat >"$DEMO/settings.json" <<'JSON'
{"statusLine":{"type":"command","command":"if command -v bosun >/dev/null; then exec bosun statusline -- sh -c 'cat >/dev/null; printf bosun-demo'; else exec sh -c 'cat >/dev/null; printf bosun-demo'; fi"}}
JSON

  if [ ! -d "$DEMO/project/.git" ]; then
    (cd "$DEMO/project" && git init -q -b main . && echo "hello" >a.txt && git add a.txt \
      && git -c user.name=demo -c user.email=demo@example.com commit -q -m init)
  fi

  demo_env "$BIN" install --tmux-conf "$DEMO/none" --settings "$DEMO/none" >"$DEMO/install.txt"
  [ -f "$DEMO/data/tmux.conf" ] || die "bosun install did not write the tmux conf"

  tm new-session -d -s "$SESSION" -n shell -x 200 -y 50 -c "$DEMO/project"
  tm set -g status-interval 1
  tm set -g status-right-length 200
  if [ "${DEMO_THEMEPACK:-1}" != 0 ] && [ -f "$THEME" ]; then tm source-file "$THEME"; fi
  tm source-file "$DEMO/data/tmux.conf"

  cat <<MSG
bosun demo is up (private tmux server "$SOCK", state in $DEMO).

  scripts/demo.sh attach           look at the bar
  scripts/demo.sh agent ABC-1      open a Claude Code window tagged ABC-1
  scripts/demo.sh bosun status     run bosun against the demo's state
  scripts/demo.sh down             stop and check nothing outside changed

Inside the demo the prefix is C-b: C-b W opens the popup, C-b a jumps to what is waiting.
MSG
}

cmd_agent() {
  running || die "not up (run: scripts/demo.sh up)"
  local task="" a
  if [ $# -gt 0 ] && [ "$1" != "--" ]; then task="$1"; shift; fi
  if [ $# -gt 0 ] && [ "$1" = "--" ]; then shift; fi
  local name="${task:-agent-$(date +%H%M%S)}"
  local cmd="claude --plugin-dir '$REPO/plugin' --settings '$DEMO/settings.json' --model haiku --permission-mode default"
  for a in "$@"; do cmd+=" $(printf '%q' "$a")"; done
  if [ -n "$task" ]; then
    tm new-window -t "$SESSION" -n "$name" -c "$DEMO/project" -e "BOSUN_TASK=$task" "exec $cmd"
  else
    tm new-window -t "$SESSION" -n "$name" -c "$DEMO/project" "exec $cmd"
  fi
  echo "opened window $name (attach with: scripts/demo.sh attach)"
}

cmd_attach() {
  running || die "not up (run: scripts/demo.sh up)"
  exec env -u TMUX tmux -L "$SOCK" -f /dev/null attach -t "$SESSION"
}

cmd_down() {
  local rc=0
  if running; then tm kill-server || true; fi
  rm -f "/tmp/tmux-$(id -u)/$SOCK"
  if [ -f "$DEMO/claude-link.before" ]; then
    local now s
    now="$(readlink "$CLAUDE_LINK" 2>/dev/null || echo '(not a symlink)')"
    if [ "$now" != "$(cat "$DEMO/claude-link.before")" ]; then
      echo "demo: FAIL ~/.local/bin/claude moved during this run: $(cat "$DEMO/claude-link.before") -> $now" >&2
      rc=1
    else
      echo "ok: ~/.local/bin/claude unchanged ($now)"
    fi
    if [ "$(real_bosun_dirs)" = "$(cat "$DEMO/real-dirs.before")" ]; then
      echo "ok: no real bosun dir was created"
    else
      echo "demo: FAIL real bosun dirs changed:" >&2
      real_bosun_dirs >&2
      rc=1
    fi
    s="$(stat -c '%Y %s' "$HOME/.claude/settings.json" 2>/dev/null || echo none)"
    if [ "$s" = "$(cat "$DEMO/settings-stat.before")" ]; then
      echo "ok: ~/.claude/settings.json unchanged"
    else
      echo "demo: FAIL ~/.claude/settings.json changed" >&2
      rc=1
    fi
  fi
  echo "stopped. state is kept in $DEMO (scripts/demo.sh reset deletes it)"
  return $rc
}

cmd_reset() {
  cmd_down || true
  # Refuse to delete anything that is not a demo dir this script creates.
  case "$DEMO" in
    */bosun-demo-*|*/bosun-demo) ;;
    *) die "refusing to delete $DEMO" ;;
  esac
  if [ -L "$DEMO" ]; then die "refusing to delete a symlink: $DEMO"; fi
  rm -rf "$DEMO"
  echo "deleted $DEMO"
}

cmd_bosun() {
  local sock pid
  sock="$(tm display-message -p '#{socket_path}' 2>/dev/null || true)"
  pid="$(tm display-message -p '#{pid}' 2>/dev/null || true)"
  if [ -n "$sock" ]; then
    demo_env env "TMUX=$sock,$pid,0" "$BIN" "$@"
  else
    # No server: point TMUX at a dead socket so bosun never reaches your real one.
    demo_env env "TMUX=$DEMO/no-server,1,0" "$BIN" "$@"
  fi
}

case "${1:-}" in
  up) shift; cmd_up "$@" ;;
  agent) shift; cmd_agent "$@" ;;
  attach) shift; cmd_attach "$@" ;;
  down) shift; cmd_down "$@" ;;
  reset) shift; cmd_reset "$@" ;;
  bosun) shift; cmd_bosun "$@" ;;
  env)
    echo "export BOSUN_STATE_DIR=$DEMO/state BOSUN_CACHE_DIR=$DEMO/cache BOSUN_DATA_DIR=$DEMO/data BOSUN_CONFIG_DIR=$DEMO/config"
    echo "export PATH=$REPO/bin:\$PATH" ;;
  *) sed -n '2,16p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
