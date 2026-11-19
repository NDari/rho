"""A minimal Rho extension in Python: one tool, no dependencies.

Rho talks to extensions with JSON-RPC 2.0, one JSON object per line on
stdin/stdout. Anything printed for humans must go to stderr.
"""
import json
import sys


def send(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def handle(method, params):
    if method == "initialize":
        return {
            "protocol_version": 1,
            "name": "wordcount",
            "version": "0.1.0",
            "tools": [{
                "name": "word_count",
                "description": "Count the words, lines and characters in a text file.",
                "parameters": {
                    "type": "object",
                    "properties": {"path": {"type": "string", "description": "File to count"}},
                    "required": ["path"],
                },
                "read_only": True,
            }],
        }
    if method == "tool/execute":
        path = params["args"].get("path", "")
        try:
            with open(path, encoding="utf-8") as f:
                text = f.read()
        except OSError as e:
            return {"content": [{"type": "text", "text": str(e)}], "is_error": True}
        words, lines = len(text.split()), text.count("\n")
        out = f"{path}: {words} words, {lines} lines, {len(text)} characters"
        return {"content": [{"type": "text", "text": out}]}
    if method == "shutdown":
        return {}
    raise KeyError(method)


for line in sys.stdin:
    msg = json.loads(line)
    if "id" not in msg:
        continue  # notification
    try:
        send({"jsonrpc": "2.0", "id": msg["id"], "result": handle(msg["method"], msg.get("params") or {})})
    except KeyError:
        send({"jsonrpc": "2.0", "id": msg["id"], "error": {"code": -32601, "message": "unknown method"}})
