#!/usr/bin/env python3
"""Render results.json as Markdown with collapsible tool breakdowns (stdout)."""
import collections
import html
import json
import os
import statistics as st
from datetime import datetime, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
D = json.load(open(os.path.join(HERE, "results.json")))
RUNS, TASKS = D["runs"], D["tasks"]
V = ("without", "with")
NAME = {"without": "without explorerepo", "with": "with explorerepo"}


def sel(task=None, variant=None):
    return [r for r in RUNS if (task is None or r["task"] == task) and (variant is None or r["variant"] == variant)]


def mean(rs, f):
    return st.mean(f(r) for r in rs)


def rng(rs, f, fmt="{:.0f}"):
    xs = [f(r) for r in rs]
    return f"{fmt.format(st.mean(xs))} ({fmt.format(min(xs))}–{fmt.format(max(xs))})"


def k(x):
    return f"{x / 1000:.0f}k" if x >= 10000 else f"{x / 1000:.1f}k"


def pct(a, b):
    return f"{(b - a) / a * 100:+.0f}%"


METRICS = [
    ("tool calls", lambda r: r["tool_calls"], "{:.1f}"),
    ("model requests", lambda r: r["steps"], "{:.1f}"),
    ("tokens (in + out)", lambda r: r["tokens_total"], None),
    ("wall time (s)", lambda r: r["wall_s"], "{:.1f}"),
]

out = []
w = out.append
n = len(sel(TASKS[0]["id"], "with"))
days = sorted({datetime.fromtimestamp(r["start_ms"] / 1000, timezone.utc).date().isoformat() for r in RUNS})
models = ", ".join(f"`{m}`" for m in sorted({r["model"] for r in RUNS}))
versions = ", ".join(sorted({r["opencode"] for r in RUNS}))
w("# explorerepo benchmark\n")
w(f"Measured on {' to '.join(dict.fromkeys([days[0], days[-1]]))}: {versions} headless (`opencode run --standalone --auto --format json`) "
  f"with {models}, {len(TASKS)} tasks × 2 variants × {n} runs = {len(RUNS)} runs, "
  "each in a fresh Docker container (Debian, git, curl, ripgrep, python3). Both variants have the same tools, including "
  "web search (granted in advance) and webfetch. The only difference: **with** has the `explorerepo` binary on `PATH` and "
  "the line from `explorerepo help agents` as the global `AGENTS.md`; **without** has neither and no `AGENTS.md`. "
  "explorerepo used anonymous HTTPS (no ssh key in the container). Harness: [`bench/`](../bench).\n")

w("Reproduce: `bench/setup.sh` (builds the image, exports only the opencode-go credential), `REPS=3 PAR=4 "
  "python3 bench/run.py`, `python3 bench/analyze.py`, `python3 bench/report.py > docs/BENCHMARK.md`. Each container "
  "imports the credential and pre-answers opencode's one-time web search prompt (it otherwise cancels in headless "
  "runs) by setting `websearch:provider` to `\"random\"` in the `kv` table of `opencode.db`; see `bench/entry.sh`.\n")
w("## Overall\n")
w("Means per run over all tasks.\n")
w("| metric | without | with | change |")
w("|---|---|---|---|")
for label, f, fmt in METRICS:
    a, b = mean(sel(variant="without"), f), mean(sel(variant="with"), f)
    fa = k(a) if fmt is None else fmt.format(a)
    fb = k(b) if fmt is None else fmt.format(b)
    w(f"| {label} | {fa} | {fb} | {pct(a, b)} |")
a, b = mean(sel(variant="without"), lambda r: r["cost_usd"]), mean(sel(variant="with"), lambda r: r["cost_usd"])
w(f"| list-price cost (USD) | {a:.4f} | {b:.4f} | {pct(a, b)} |")
ca, cb = sum(r["correct"] for r in sel(variant="without")), sum(r["correct"] for r in sel(variant="with"))
w(f"| correct answers | {ca}/{len(sel(variant='without'))} | {cb}/{len(sel(variant='with'))} | |\n")
w("Tokens are input (uncached + cache reads) plus output (including reasoning), summed over every model request of "
  "the run; each request re-sends the conversation, so tokens grow with the number of requests and the size of what "
  "the tools return. Cost uses the models.dev list price ($0.15 / 1M input, $0.003 / 1M cache read, $0.60 / 1M "
  "output); the Go subscription itself reports $0. Correctness is a check that the answer contains the facts "
  "verified in the source beforehand (listed per task below).\n")

w("## Per task\n")
w("Mean (min–max) over the runs.\n")
w("| task | variant | tool calls | requests | tokens | time (s) | correct |")
w("|---|---|---|---|---|---|---|")
for t in TASKS:
    for v in V:
        rs = sel(t["id"], v)
        tok = [r["tokens_total"] for r in rs]
        w(f"| {t['id']} | {v} | {rng(rs, lambda r: r['tool_calls'], '{:.1f}')} | {rng(rs, lambda r: r['steps'], '{:.1f}')} "
          f"| {k(st.mean(tok))} ({k(min(tok))}–{k(max(tok))}) | {rng(rs, lambda r: r['wall_s'], '{:.0f}')} "
          f"| {sum(r['correct'] for r in rs)}/{len(rs)} |")
w("")


def counts_table(rs):
    c = collections.Counter(x["label"] for r in rs for x in r["tools"])
    lines = ["| tool | calls | per run |", "|---|---|---|"]
    for lab, cnt in c.most_common():
        lines.append(f"| `{lab}` | {cnt} | {cnt / len(rs):.1f} |")
    return "\n".join(lines)


w("## Tool usage\n")
w("Shell commands (`shell`) and code-mode scripts (`execute`, opencode's JavaScript tool) are labelled by what they "
  "did: `raw file download` fetches raw.githubusercontent.com, `GitHub API` hits api.github.com, `local file ops` "
  "greps/reads files that an earlier download saved.\n")
for v in V:
    rs = sel(variant=v)
    err = sum(x["status"] == "error" for r in rs for x in r["tools"])
    w(f"<details>\n<summary><b>{NAME[v]}</b>: {sum(r['tool_calls'] for r in rs)} calls in {len(rs)} runs, {err} failed</summary>\n")
    w(counts_table(rs))
    w("\n</details>\n")

w("## Breakdown per task and run\n")
for t in TASKS:
    w(f"### {t['id']}\n")
    w(f"> {t['prompt']}\n")
    w(f"Expected: {t['expect']}. Repo {'named' if t['named'] else 'not named'} in the prompt.\n")
    for v in V:
        rs = sel(t["id"], v)
        w(f"<details>\n<summary><b>{v}</b>: {mean(rs, lambda r: r['tool_calls']):.1f} calls, "
          f"{k(mean(rs, lambda r: r['tokens_total']))} tokens, {mean(rs, lambda r: r['wall_s']):.0f} s per run</summary>\n")
        w(counts_table(rs) + "\n")
        for r in sorted(rs, key=lambda r: r["rep"]):
            tk = r["tokens"]
            w(f"<details>\n<summary>run {r['rep'] + 1}: {r['tool_calls']} calls, {r['steps']} requests, "
              f"{k(r['tokens_total'])} tokens, {r['wall_s']:.0f} s, {'correct' if r['correct'] else 'incorrect'}</summary>\n")
            w(f"Tokens: {tk['input']:,} input, {tk['cache_read']:,} cache read, {tk['output']:,} output, "
              f"{tk['reasoning']:,} reasoning.\n")
            w("| # | tool | status | input |")
            w("|---|---|---|---|")
            for i, x in enumerate(r["tools"], 1):
                s = x["input"]
                s = html.escape(s[:220] + ("…" if len(s) > 220 else "")).replace("|", "\\|")
                w(f"| {i} | `{x['label']}` | {x['status']} | <code>{s}</code> |")
            w("\n</details>\n")
        w("</details>\n")

print("\n".join(out))
