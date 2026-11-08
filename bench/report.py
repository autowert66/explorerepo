#!/usr/bin/env python3
"""Render the result files of every task set as one Markdown document with collapsible tool breakdowns (stdout)."""
import collections
import html
import json
import os
import statistics as st
from datetime import datetime, timezone

HERE = os.path.dirname(os.path.abspath(__file__))
# (results file, tier title, one-line description)
TIERS = [
    ("results.json", "Small repositories",
     "Six questions about single, mid-sized repositories (ripgrep, bubbletea, httpx, fzf, bat, zod). "
     "The component is named in the question, so the file is easy to guess."),
    ("results-hard.json", "Large repositories",
     "Nine questions about the Linux kernel, Android (AOSP), Chromium and Kubernetes. Most name a component but not "
     "the file, several depend on code that moved recently, and one (zram) names no file or directory."),
]
V = ("without", "with")
NAME = {"without": "without explorerepo", "with": "with explorerepo"}


def mean(rs, f):
    return st.mean(f(r) for r in rs)


def rng(rs, f, fmt="{:.0f}"):
    xs = [f(r) for r in rs]
    return f"{fmt.format(st.mean(xs))} ({fmt.format(min(xs))}–{fmt.format(max(xs))})"


def k(x):
    return f"{x / 1000:.0f}k" if x >= 10000 else f"{x / 1000:.1f}k"


def rng_k(rs, f):
    xs = [f(r) for r in rs]
    return f"{k(st.mean(xs))} ({k(min(xs))}–{k(max(xs))})"


def pct(a, b):
    return f"{(b - a) / a * 100:+.0f}%"


def out_tokens(r):
    return r["tokens"]["output"] + r["tokens"]["reasoning"]


METRICS = [
    ("tool calls", lambda r: r["tool_calls"], "{:.1f}"),
    ("model requests", lambda r: r["steps"], "{:.1f}"),
    ("input tokens, uncached", lambda r: r["tokens"]["input"], None),
    ("input tokens, cache read", lambda r: r["tokens"]["cache_read"], None),
    ("output tokens (with reasoning)", out_tokens, None),
    ("wall time (s)", lambda r: r["wall_s"], "{:.1f}"),
    ("list-price cost (USD)", lambda r: r["cost_usd"], "{:.4f}"),
]

tiers = []
for fname, title, desc in TIERS:
    D = json.load(open(os.path.join(HERE, fname)))
    tiers.append({"title": title, "desc": desc, "runs": D["runs"], "tasks": D["tasks"]})

out = []
w = out.append


def sel(t, task=None, variant=None):
    return [r for r in t["runs"] if (task is None or r["task"] == task) and (variant is None or r["variant"] == variant)]


allruns = [r for t in tiers for r in t["runs"]]
models = ", ".join(f"`{m}`" for m in sorted({r["model"] for r in allruns}))
versions = ", ".join(sorted({r["opencode"] for r in allruns}))
w("# explorerepo benchmark\n")
w(f"Agents with and without explorerepo answered {sum(len(t['tasks']) for t in tiers)} questions about the source "
  f"of real repositories, three runs per question and variant ({len(allruns)} runs). Each run used {versions} headless "
  f"(`opencode run --standalone --auto --format json`) with {models} in a fresh Docker container (Debian, git, curl, "
  "ripgrep, python3). Both variants have the same tools, including web search (granted in advance) and webfetch. "
  "The only difference: **with** has the `explorerepo` binary on `PATH` and the line from `explorerepo help agents` as "
  "the global `AGENTS.md`; **without** has neither and no `AGENTS.md`. explorerepo used anonymous HTTPS (no ssh key in "
  "the container). It never clones: it fetches only the trees and files a command reads, into a cache that starts "
  "empty in every fresh container, so each run pays for all of its own network fetches.\n")
w("Correctness is a regular-expression check that the answer contains the facts verified in the source beforehand "
  "(listed per task below). Tokens are summed over every model request of a run and reported by type, because the types "
  "are billed very differently: uncached input, cache reads (the part of the conversation the provider already holds, "
  "re-sent on each request) and output (including reasoning). Cost uses the models.dev list price ($0.15 / 1M input, "
  "$0.003 / 1M cache read, $0.60 / 1M output); the Go subscription itself reports $0. Figures are means per run.\n")
w("Reproduce with the harness in [`bench/`](../bench): `bench/setup.sh` builds the image and exports only the "
  "opencode-go credential; `REPS=3 PAR=4 python3 bench/run.py` runs the small set and "
  "`TASKS_FILE=tasks-hard.json RUNS_DIR=runs-hard TIMEOUT=900 REPS=3 PAR=4 python3 bench/run.py` the large set; "
  "`bench/analyze.py` (with the same `TASKS_FILE`, `RUNS_DIR` and `RESULTS=results-hard.json` for the large set) "
  "summarises them and `python3 bench/report.py > docs/BENCHMARK.md` renders this page. Each container imports the "
  "credential and pre-answers opencode's one-time web search prompt (it otherwise cancels in headless runs) by setting "
  "`websearch:provider` to `\"random\"` in the `kv` table of `opencode.db`; see `bench/entry.sh`.\n")

for t in tiers:
    days = sorted({datetime.fromtimestamp(r["start_ms"] / 1000, timezone.utc).date().isoformat() for r in t["runs"]})
    n = len(sel(t, t["tasks"][0]["id"], "with"))
    w(f"## {t['title']}\n")
    w(f"{t['desc']} Measured on {' to '.join(dict.fromkeys([days[0], days[-1]]))}: "
      f"{len(t['tasks'])} tasks × 2 variants × {n} runs = {len(t['runs'])} runs.\n")
    w("| metric | without | with | change |")
    w("|---|---|---|---|")
    for label, f, fmt in METRICS:
        a, b = mean(sel(t, variant="without"), f), mean(sel(t, variant="with"), f)
        fa, fb = (k(a), k(b)) if fmt is None else (fmt.format(a), fmt.format(b))
        w(f"| {label} | {fa} | {fb} | {pct(a, b)} |")
    ca, cb = sum(r["correct"] for r in sel(t, variant="without")), sum(r["correct"] for r in sel(t, variant="with"))
    w(f"| correct answers | {ca}/{len(sel(t, variant='without'))} | {cb}/{len(sel(t, variant='with'))} | |\n")
    w("Mean (min–max) per task.\n")
    w("| task | variant | tool calls | requests | input | cache read | output | time (s) | correct |")
    w("|---|---|---|---|---|---|---|---|---|")
    for task in t["tasks"]:
        for v in V:
            rs = sel(t, task["id"], v)
            w(f"| {task['id']} | {v} | {rng(rs, lambda r: r['tool_calls'], '{:.1f}')} "
              f"| {rng(rs, lambda r: r['steps'], '{:.1f}')} | {rng_k(rs, lambda r: r['tokens']['input'])} "
              f"| {rng_k(rs, lambda r: r['tokens']['cache_read'])} | {rng_k(rs, out_tokens)} "
              f"| {rng(rs, lambda r: r['wall_s'], '{:.0f}')} | {sum(r['correct'] for r in rs)}/{len(rs)} |")
    w("")

w("## Limits\n")
w("- One model and one agent harness. A stronger model may need fewer exploratory calls without explorerepo.\n"
  "- Both variants answered every question correctly, so the runs measure cost and time, not accuracy.\n"
  "- The regular-expression checks match key values, not reasoning, so a correct value cited from the wrong place would pass.\n"
  "- Only one question (zram) names no file or directory. Searching a whole very large tree is the weak case: "
  "a recursive `grep -rl` over Android's `frameworks/base` did not finish within explorerepo's default 60 s "
  "`--timeout` when tried outside this task set.\n"
  "- Wall time includes network and model latency and varies between runs of the same question; the ranges in the "
  "per-task tables show how much.\n")


def counts_table(rs):
    c = collections.Counter(x["label"] for r in rs for x in r["tools"])
    lines = ["| tool | calls | per run |", "|---|---|---|"]
    for lab, cnt in c.most_common():
        lines.append(f"| `{lab}` | {cnt} | {cnt / len(rs):.1f} |")
    return "\n".join(lines)


w("## Tool usage\n")
w("Shell commands (`shell`) and code-mode scripts (`execute`, opencode's JavaScript tool) are labelled by what they "
  "did: `raw file download` fetches raw.githubusercontent.com, `GitHub API` hits api.github.com, `local file ops` "
  "greps/reads files that an earlier download saved, `other HTTP` covers other fetches such as googlesource.\n")
for t in tiers:
    for v in V:
        rs = sel(t, variant=v)
        err = sum(x["status"] == "error" for r in rs for x in r["tools"])
        w(f"<details>\n<summary><b>{t['title']}, {NAME[v]}</b>: {sum(r['tool_calls'] for r in rs)} calls in "
          f"{len(rs)} runs, {err} failed</summary>\n")
        w(counts_table(rs))
        w("\n</details>\n")

w("## Breakdown per task and run\n")
for t in tiers:
    w(f"### {t['title']}\n")
    for task in t["tasks"]:
        w(f"#### {task['id']}\n")
        w(f"> {task['prompt']}\n")
        w(f"Expected: {task['expect']}. Repository {'named' if task['named'] else 'not named'} in the prompt.\n")
        for v in V:
            rs = sel(t, task["id"], v)
            w(f"<details>\n<summary><b>{v}</b>: {mean(rs, lambda r: r['tool_calls']):.1f} calls, "
              f"{k(mean(rs, lambda r: r['tokens']['input']))} input, {k(mean(rs, lambda r: r['tokens']['cache_read']))} "
              f"cache read, {k(mean(rs, out_tokens))} output tokens, {mean(rs, lambda r: r['wall_s']):.0f} s per run</summary>\n")
            w(counts_table(rs) + "\n")
            for r in sorted(rs, key=lambda r: r["rep"]):
                tk = r["tokens"]
                w(f"<details>\n<summary>run {r['rep'] + 1}: {r['tool_calls']} calls, {r['steps']} requests, "
                  f"{k(tk['input'])} input, {k(tk['cache_read'])} cache read, {k(out_tokens(r))} output tokens, "
                  f"{r['wall_s']:.0f} s, {'correct' if r['correct'] else 'incorrect'}</summary>\n")
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
