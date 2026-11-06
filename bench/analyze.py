#!/usr/bin/env python3
"""Summarise runs/*/events.jsonl into results.json."""
import glob
import json
import os
import re
import statistics as st

HERE = os.path.dirname(os.path.abspath(__file__))
TASKS = {t["id"]: t for t in json.load(open(os.path.join(HERE, "tasks.json")))}
# opencode-go list price for deepseek-v4.1-flash, USD per 1M tokens (from models.dev)
PRICE = {"input": 0.15, "output": 0.6, "cache_read": 0.003}


def classify(tool, inp):
    """Label a tool call; shell commands and code-mode scripts are split by what they do."""
    if tool not in ("shell", "bash", "execute"):
        return tool
    src = (inp.get("command") or inp.get("code") or "").strip()
    if "explorerepo" in src:
        what = "explorerepo"
    elif re.search(r"\bgit\s+clone\b", src):
        what = "git clone"
    elif re.search(r"\bgit\s", src):
        what = "git (other)"
    elif "api.github.com" in src or re.search(r"\bgh\s+api\b", src):
        what = "GitHub API"
    elif "raw.githubusercontent" in src or "cdn.jsdelivr" in src or "unpkg.com" in src:
        what = "raw file download"
    elif re.search(r"\b(curl|wget)\b|fetch\(", src):
        what = "other HTTP"
    elif re.search(r"\b(pip|npm|pnpm|cargo|go)\s+(install|get|download)\b", src):
        what = "package download"
    elif re.search(r"tool-output/|\b(grep|sed|cat|head|tail|rg)\b", src):
        what = "local file ops"
    else:
        what = "other"
    return f"{tool}: {what}"


def parse(path):
    tid, variant, rep = os.path.basename(path).split("__")
    meta = json.load(open(os.path.join(path, "meta.json")))
    tools, steps, text = [], 0, []
    tok = {"input": 0, "output": 0, "reasoning": 0, "cache_read": 0, "cache_write": 0}
    errors = []
    for line in open(os.path.join(path, "events.jsonl"), errors="replace"):
        try:
            e = json.loads(line)
        except json.JSONDecodeError:
            continue
        p = e.get("part", {})
        if e["type"] == "tool_use":
            s = p["state"]
            inp = s.get("input") or {}
            tools.append({"tool": p["tool"], "label": classify(p["tool"], inp), "status": s.get("status"),
                          "input": " ".join(str(inp.get("command") or inp.get("code") or inp.get("url")
                                                     or inp.get("query") or json.dumps(inp)).split())[:600]})
        elif e["type"] == "step_finish":
            steps += 1
            t = p.get("tokens", {})
            tok["input"] += t.get("input", 0)
            tok["output"] += t.get("output", 0)
            tok["reasoning"] += t.get("reasoning", 0)
            tok["cache_read"] += t.get("cache", {}).get("read", 0)
            tok["cache_write"] += t.get("cache", {}).get("write", 0)
        elif e["type"] == "text":
            text.append(p.get("text", ""))
        elif e["type"] == "error":
            errors.append(json.dumps(e.get("error"))[:300])
    answer = "\n".join(text).strip()
    checks = TASKS[tid]["checks"]
    passed = [bool(re.search(c, answer)) for c in checks]
    total_in = tok["input"] + tok["cache_read"]
    cost = (tok["input"] * PRICE["input"] + (tok["output"] + tok["reasoning"]) * PRICE["output"]
            + tok["cache_read"] * PRICE["cache_read"]) / 1e6
    return {
        "task": tid, "variant": variant, "rep": int(rep), "model": meta["model"], "opencode": meta["opencode"],
        "start_ms": meta["start_ms"], "exit": meta["exit"],
        "wall_s": meta["wall_ms"] / 1000, "steps": steps, "tool_calls": len(tools),
        "tools": tools, "tokens": tok, "tokens_in_total": total_in,
        "tokens_total": total_in + tok["output"] + tok["reasoning"], "cost_usd": cost,
        "correct": all(passed) and meta["exit"] == 0, "checks": passed, "answer": answer,
        "errors": errors,
    }


runs = [parse(p) for p in sorted(glob.glob(os.path.join(HERE, "runs", "*")))
        if os.path.exists(os.path.join(p, "meta.json"))]
json.dump({"tasks": list(TASKS.values()), "runs": runs, "price": PRICE},
          open(os.path.join(HERE, "results.json"), "w"), indent=1)

for tid in TASKS:
    for v in ("without", "with"):
        rs = [r for r in runs if r["task"] == tid and r["variant"] == v]
        if not rs:
            continue
        m = lambda k: st.mean(r[k] for r in rs)
        print(f"{tid:15} {v:8} n={len(rs)} calls={m('tool_calls'):5.1f} steps={m('steps'):5.1f} "
              f"tok={m('tokens_total'):9.0f} time={m('wall_s'):6.1f}s ok={sum(r['correct'] for r in rs)}/{len(rs)}")
