#!/usr/bin/env python3
"""
PreToolUse guard — the gl_journal_ ledger tables are APPEND-ONLY.

Project rule: never UPDATE, DELETE or TRUNCATE rows in the general-ledger tables
(gl_journal_entries, gl_journal_lines), and never weaken their append-only guard
(no DROP/DISABLE TRIGGER on them, no `SET session_replication_role = replica`, which
turns the guard off). Corrections to the ledger are made as REVERSING journal entries
(INSERT), not in-place edits.

This hook inspects each Bash command (and any .sql files passed to psql via -f/--file)
and, on a match, returns permissionDecision "ask" — which forces a confirmation prompt
in EVERY permission mode, including Auto / acceptEdits. So such an action can never run
silently: it proceeds only if the user explicitly approves the prompt (i.e. only when the
user has explicitly requested that exact change).

Fails open on unparseable hook input (so it never wedges unrelated Bash commands), but
blocks whenever it can actually read the command and sees a forbidden pattern.
"""
import sys
import os
import re
import json
import shlex


def emit_ask(reason: str) -> None:
    print(json.dumps({
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "ask",
            "permissionDecisionReason": reason,
        }
    }))
    sys.exit(0)


def main() -> None:
    try:
        data = json.load(sys.stdin)
    except Exception:
        sys.exit(0)  # can't parse input — do not interfere with the command

    if data.get("tool_name") != "Bash":
        sys.exit(0)
    command = ((data.get("tool_input") or {}).get("command") or "")
    if not command.strip():
        sys.exit(0)

    texts = [command]

    # Also scan SQL files handed to psql via -f <path> / --file <path> / -f<path> / --file=<path>.
    try:
        tokens = shlex.split(command)
    except Exception:
        tokens = command.split()
    i = 0
    while i < len(tokens):
        tok = tokens[i]
        path = None
        if tok in ("-f", "--file") and i + 1 < len(tokens):
            path = tokens[i + 1]
            i += 1
        elif tok.startswith("--file="):
            path = tok[len("--file="):]
        elif tok.startswith("-f") and len(tok) > 2:
            path = tok[2:]
        if path:
            roots = [path]
            if not os.path.isabs(path):
                base = os.environ.get("CLAUDE_PROJECT_DIR", os.getcwd())
                roots += [os.path.join(base, path), os.path.join(os.getcwd(), path)]
            for candidate in roots:
                try:
                    with open(candidate, "r", errors="ignore") as fh:
                        texts.append(fh.read())
                    break
                except Exception:
                    continue
        i += 1

    blob = "\n".join(texts)

    # A gl_journal_ table reference: optional ONLY, optional schema qualifier, optional quotes.
    gl = r'(?:only\s+)?(?:"?\w+"?\.)?"?gl_journal_\w*'
    checks = [
        (re.compile(r'\bupdate\s+' + gl, re.I | re.S), "UPDATE a gl_journal_ table"),
        (re.compile(r'\bdelete\s+from\s+' + gl, re.I | re.S), "DELETE FROM a gl_journal_ table"),
        (re.compile(r'\btruncate\s+(?:table\s+)?' + gl, re.I | re.S), "TRUNCATE a gl_journal_ table"),
        (re.compile(r'session_replication_role\s*=\s*\'?replica\'?', re.I),
         "SET session_replication_role = replica (disables the append-only guard)"),
    ]
    # DROP/DISABLE TRIGGER only counts when a gl_journal_ table (or its trg_gl_ trigger) is in play.
    if re.search(r'gl_journal_|trg_gl_', blob, re.I):
        checks.append((re.compile(r'\bdrop\s+trigger\b', re.I), "DROP TRIGGER on a gl_journal_ table"))
        checks.append((re.compile(r'\bdisable\s+trigger\b', re.I), "DISABLE TRIGGER on a gl_journal_ table"))

    for rx, what in checks:
        if rx.search(blob):
            emit_ask(
                "PROJECT RULE — the gl_journal_ ledger is APPEND-ONLY. This command would "
                + what + ". No UPDATE / DELETE / TRUNCATE on gl_journal_entries or "
                "gl_journal_lines, and the append-only guard must not be weakened (no "
                "DROP/DISABLE TRIGGER, no session_replication_role=replica). Corrections "
                "are made as REVERSING journal entries (INSERT), never in-place edits. Do "
                "NOT proceed unless the user has EXPLICITLY requested this exact change in "
                "the current conversation — if they have, they can approve this prompt."
            )

    sys.exit(0)


if __name__ == "__main__":
    main()
