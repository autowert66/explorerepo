#!/usr/bin/env python3
"""Run every (task, variant, rep) in its own fresh container; results land in runs/<id>/."""
import concurrent.futures as cf
import json
import os
import random
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
TASKS = json.load(open(os.path.join(HERE, "tasks.json")))
REPS = int(os.environ.get("REPS", "3"))
PAR = int(os.environ.get("PAR", "4"))
MODEL = os.environ.get("MODEL", "opencode-go/deepseek-v4.1-flash")

jobs = [(t["id"], v, r) for t in TASKS for v in ("with", "without") for r in range(REPS)]
random.Random(7).shuffle(jobs)  # interleave variants so time-of-day effects hit both
prompts = {t["id"]: t["prompt"] for t in TASKS}


def run(job):
    tid, variant, rep = job
    out = os.path.join(HERE, "runs", f"{tid}__{variant}__{rep}")
    if os.path.exists(os.path.join(out, "meta.json")):
        return job, "skip"
    os.makedirs(out, exist_ok=True)
    os.chmod(out, 0o777)
    cmd = ["docker", "run", "--rm", "-e", f"VARIANT={variant}", "-e", f"PROMPT={prompts[tid]}", "-e", f"MODEL={MODEL}",
           "-v", f"{HERE}/build/go.json:/run/go.json:ro", "-v", f"{out}:/out", "ocbench", "entry.sh"]
    p = subprocess.run(cmd, capture_output=True, text=True)
    if p.returncode:
        open(os.path.join(out, "docker.err"), "w").write(p.stdout + p.stderr)
    return job, p.returncode


with cf.ThreadPoolExecutor(PAR) as ex:
    for i, (job, rc) in enumerate(ex.map(run, jobs), 1):
        print(f"[{i}/{len(jobs)}] {job} rc={rc}", flush=True)
