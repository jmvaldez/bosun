#!/usr/bin/env python3
"""Redact a captured Claude Code JSON or JSONL file per kit/CHECKLIST.md.

Key names and numbers survive. Ids become stable placeholders. Paths become
fixed placeholders. Every other string becomes "<redacted>", except values of
keys known to hold enums (event names, notification types, tool names, ...).

usage: redact.py IN OUT   (IN may be JSON, JSONL, or "-" for stdin)
"""
import json
import sys

ID_KEYS = {"session_id", "sessionId", "agent_id", "prompt_id", "tool_use_id", "id", "launch_session_id"}
PATH_KEYS = {
    "cwd": "/work/project", "current_dir": "/work/project", "project_dir": "/work/project",
    "transcript_path": "/redacted/transcript.jsonl", "agent_transcript_path": "/redacted/agent-transcript.jsonl",
    "scratchpad_dir": "/redacted/scratchpad",
}
# Values of these keys are enums or product names, never user text.
ENUM_KEYS = {
    "hook_event_name", "notification_type", "type", "status", "source", "reason", "tool_name",
    "agent_type", "subagent_type", "permission_mode", "kind", "state", "level", "version",
    "display_name", "ttl",
}
# model.id and output_style.name are enums too; their parent key identifies
# them, so walk() special-cases them.


class Redactor:
    def __init__(self):
        self.ids = {}

    def placeholder(self, key, value):
        # Keyed by value, so ids that are equal in the capture (a background
        # task id equals its agent_id) stay equal after redaction.
        if value not in self.ids:
            # "ession" matches session_id, sessionId and launch_session_id.
            short = "agent" if key == "agent_id" else "prompt" if key == "prompt_id" else \
                "tool" if key == "tool_use_id" else "session" if "ession" in key else "id"
            n = sum(1 for p in self.ids.values() if p.startswith(f"<{short}-")) + 1
            self.ids[value] = f"<{short}-{n}>"
        return self.ids[value]

    def walk(self, v, key=None, parent=None):
        if isinstance(v, dict):
            return {k: self.walk(x, k, key) for k, x in v.items()}
        if isinstance(v, list):
            return [self.walk(x, key, parent) for x in v]
        if isinstance(v, str):
            if key == "id" and parent == "model":
                return v
            if key in ID_KEYS:
                return self.placeholder(key, v)
            if key in PATH_KEYS:
                return PATH_KEYS[key]
            if key in ENUM_KEYS:
                return v
            if key == "name" and parent == "output_style":
                return v
            return "<redacted>"
        return v  # numbers, bools, null


def load(path):
    text = sys.stdin.read() if path == "-" else open(path, encoding="utf-8").read()
    try:
        return [json.loads(text)], False
    except json.JSONDecodeError:
        return [json.loads(line) for line in text.splitlines() if line.strip()], True


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    docs, is_lines = load(sys.argv[1])
    r = Redactor()
    out = [r.walk(d) for d in docs]
    with open(sys.argv[2], "w", encoding="utf-8") as f:
        if is_lines:
            for d in out:
                f.write(json.dumps(d, separators=(",", ":")) + "\n")
        else:
            f.write(json.dumps(out[0], indent=2) + "\n")


if __name__ == "__main__":
    main()
