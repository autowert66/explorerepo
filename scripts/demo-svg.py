#!/usr/bin/env python3
# Generates docs/demo-dark.svg and docs/demo-light.svg: an animated,
# self-contained SVG for the README, in GitHub's dark and light themes.
#
#   scripts/demo-svg.py
#
# The output is the source of truth for the README visual: edit this file, not
# the SVGs, or the next run overwrites your edit. Numbers are the cold-cache
# measurements from docs/BENCHMARK.md (torvalds/linux, grep for sched_tick and
# read it, against git clone --depth 1).
import os
from html import escape

T = 18.0  # loop length, seconds
CW = 7.83  # approx monospace char width at 13px

MONO = "ui-monospace,SFMono-Regular,Menlo,Consolas,'Liberation Mono',monospace"
SANS = "-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif"

THEMES = {
    "dark": dict(
        page_a="#0a0f1f", page_b="#0f1626",
        grid="#ffffff", grid_op=".035",
        glow_green="#3fb950", glow_green_op=".22",
        glow_blue="#2f81f7", glow_blue_op=".25",
        gbar_a="#2ea043", gbar_b="#56d364",
        obar_a="#bd561d", obar_b="#f0883e",
        shadow="#000", shadow_op=".55", shadow_dy="10", shadow_std="14",
        card="#0d1117", stroke="#30363d", rule="#21262d",
        fg="#e6edf3", dim="#7d8590", ghost="#3d444d",
        green="#3fb950", amber="#e3b341", blue="#79c0ff", purple="#d2a8ff", orange="#f0883e",
        code="#c9d1d9", comment="#8b949e", hit="#ff7b72",
        track="#161b22", panel="#0d1117", panel_op=".82", footer="#6e7681",
        glow=True,
    ),
    "light": dict(
        page_a="#ffffff", page_b="#f1f4f8",
        grid="#1f2328", grid_op=".045",
        glow_green="#2da44e", glow_green_op=".10",
        glow_blue="#2f81f7", glow_blue_op=".10",
        gbar_a="#4ac26b", gbar_b="#1a7f37",
        obar_a="#e8743b", obar_b="#bc4c00",
        shadow="#1f2328", shadow_op=".12", shadow_dy="8", shadow_std="16",
        card="#ffffff", stroke="#d0d7de", rule="#eaedf1",
        fg="#1f2328", dim="#656d76", ghost="#c3ccd5",
        green="#1a7f37", amber="#9a6700", blue="#0969da", purple="#8250df", orange="#bc4c00",
        code="#1f2328", comment="#6e7781", hit="#cf222e",
        track="#eaeef2", panel="#f6f8fa", panel_op=".9", footer="#6e7781",
        glow=False,
    ),
}


def build(theme):
    P = THEMES[theme]
    BG, FG, DIM, GHOST = P["card"], P["fg"], P["dim"], P["ghost"]
    GREEN, AMBER, BLUE = P["green"], P["amber"], P["blue"]
    PURPLE, ORANGE = P["purple"], P["orange"]
    GLOW = ' filter="url(#glow)"' if P["glow"] else ""

    css_rules, kf_rules = [], []
    state = {"n": 0}

    def pct(t):
        return f"{t / T * 100:.3f}%"

    def anim(frames, extra=""):
        """frames: list of (t_seconds, props, timing or None). Returns a class name."""
        state["n"] += 1
        name = f"a{state['n']}"
        body = []
        for t, props, timing in frames:
            tf = f"animation-timing-function:{timing};" if timing else ""
            body.append(f"{pct(t)}{{{props}{tf}}}")
        kf_rules.append(f"@keyframes {name}{{{''.join(body)}}}")
        css_rules.append(f".{name}{{animation:{name} {T}s infinite;{extra}}}")
        return name

    END_HOLD, END = 17.3, 17.8

    def appear(t, d=0.25, extra=""):
        return anim([(0, "opacity:0;", None), (t, "opacity:0;", None), (t + d, "opacity:1;", None),
                     (END_HOLD, "opacity:1;", None), (END, "opacity:0;", None), (T, "opacity:0;", None)], extra)

    def light(t, dim, lit, d=0.3):
        return anim([(0, f"fill:{dim};", None), (t, f"fill:{dim};", None), (t + d, f"fill:{lit};", None),
                     (END_HOLD, f"fill:{lit};", None), (END, f"fill:{dim};", None), (T, f"fill:{dim};", None)])

    def flash(t, dim, hot, settle):
        return anim([(0, f"fill:{dim};", None), (t, f"fill:{dim};", None), (t + 0.15, f"fill:{hot};", None),
                     (t + 0.7, f"fill:{settle};", None), (END_HOLD, f"fill:{settle};", None),
                     (END, f"fill:{dim};", None), (T, f"fill:{dim};", None)])

    def grow(t0, t1, timing="cubic-bezier(.2,.7,.3,1)"):
        return anim([(0, "transform:scaleX(0);opacity:1;", None), (t0, "transform:scaleX(0);opacity:1;", timing),
                     (t1, "transform:scaleX(1);opacity:1;", None), (END_HOLD, "transform:scaleX(1);opacity:1;", None),
                     (END, "transform:scaleX(1);opacity:0;", None), (T, "transform:scaleX(0);opacity:0;", None)],
                    "transform-box:fill-box;transform-origin:left center;")

    def typer(t0, t1, chars):
        """A cover (with cursor at its left edge) sliding right in steps to reveal typed text."""
        w = chars * CW
        return anim([(0, "transform:translateX(0);opacity:1;", None),
                     (t0, "transform:translateX(0);opacity:1;", f"steps({chars},end)"),
                     (t1, f"transform:translateX({w:.1f}px);opacity:1;", None),
                     (t1 + 0.01, f"transform:translateX({w:.1f}px);opacity:0;", None),
                     (T - 0.01, "transform:translateX(0);opacity:0;", None),
                     (T, "transform:translateX(0);opacity:1;", None)], "opacity:0;")

    out = []
    W, H = 960, 628
    o = out.append

    # ---------- palette
    o(f'''<defs>
<linearGradient id="bg" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="{P["page_a"]}"/><stop offset="1" stop-color="{P["page_b"]}"/></linearGradient>
<radialGradient id="g1" cx="0.12" cy="0.95" r="0.6"><stop offset="0" stop-color="{P["glow_green"]}" stop-opacity="{P["glow_green_op"]}"/><stop offset="1" stop-color="{P["glow_green"]}" stop-opacity="0"/></radialGradient>
<radialGradient id="g2" cx="0.92" cy="0.05" r="0.55"><stop offset="0" stop-color="{P["glow_blue"]}" stop-opacity="{P["glow_blue_op"]}"/><stop offset="1" stop-color="{P["glow_blue"]}" stop-opacity="0"/></radialGradient>
<pattern id="grid" width="24" height="24" patternUnits="userSpaceOnUse"><path d="M24 0H0V24" fill="none" stroke="{P["grid"]}" stroke-opacity="{P["grid_op"]}"/></pattern>
<linearGradient id="gbar" x1="0" x2="1"><stop offset="0" stop-color="{P["gbar_a"]}"/><stop offset="1" stop-color="{P["gbar_b"]}"/></linearGradient>
<linearGradient id="obar" x1="0" x2="1"><stop offset="0" stop-color="{P["obar_a"]}"/><stop offset="1" stop-color="{P["obar_b"]}"/></linearGradient>
<filter id="shadow" x="-10%" y="-10%" width="120%" height="130%"><feDropShadow dx="0" dy="{P["shadow_dy"]}" stdDeviation="{P["shadow_std"]}" flood-color="{P["shadow"]}" flood-opacity="{P["shadow_op"]}"/></filter>
<filter id="glow" x="-50%" y="-100%" width="200%" height="300%"><feGaussianBlur stdDeviation="3" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>
<clipPath id="termclip"><rect x="32" y="108" width="572" height="296"/></clipPath>
</defs>
<rect width="{W}" height="{H}" rx="18" fill="url(#bg)"/>
<rect width="{W}" height="{H}" rx="18" fill="url(#grid)"/>
<rect width="{W}" height="{H}" rx="18" fill="url(#g1)"/>
<rect width="{W}" height="{H}" rx="18" fill="url(#g2)"/>
<text x="34" y="50" font-family="{SANS}" font-size="27" font-weight="700" fill="{FG}">explore<tspan fill="{GREEN}">repo</tspan></text>
<text x="926" y="40" text-anchor="end" font-family="{SANS}" font-size="14.5" fill="{FG}">an agent reads <tspan font-family="{MONO}" fill="{BLUE}">torvalds/linux</tspan> without cloning it</text>
<text x="926" y="60" text-anchor="end" font-family="{SANS}" font-size="12.5" fill="{DIM}">only the trees and files the script touches cross the wire</text>
''')

    def window(x, y, w, h, title):
        o(f'''<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="11" fill="{P["shadow"]}" filter="url(#shadow)"/>
<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="11" fill="{BG}" stroke="{P["stroke"]}"/>
<path d="M{x} {y + 30}H{x + w}" stroke="{P["rule"]}"/>
<circle cx="{x + 18}" cy="{y + 15}" r="5.5" fill="#ff5f57"/><circle cx="{x + 36}" cy="{y + 15}" r="5.5" fill="#febc2e"/><circle cx="{x + 54}" cy="{y + 15}" r="5.5" fill="#28c840"/>
<text x="{x + w / 2}" y="{y + 19.5}" text-anchor="middle" font-family="{SANS}" font-size="12" fill="{DIM}">{escape(title)}</text>''')

    # ---------- terminal
    window(32, 78, 572, 328, "agent — zsh")
    X0, Y0, LH = 50, 132, 18.6
    t_type = [(0.7, 2.0), (2.25, 3.95), (4.15, 5.55)]
    ENTER = 5.75
    FETCH_DONE = ENTER + 2.0  # measured: 1.99 s wall time for this script

    cmd = [
        [("$ ", GREEN), ("explorerepo ", FG), ("--repo ", DIM), ("torvalds/linux ", BLUE), ("-c ", DIM), ("'", AMBER)],
        [("    grep -rn ", AMBER), ('"^void sched_tick"', AMBER), (" kernel/sched", AMBER)],
        [("    sed -n 5829,5837p kernel/sched/core.c'", AMBER)],
    ]
    o(f'<g font-family="{MONO}" font-size="13" clip-path="url(#termclip)">')
    cmdfade = anim([(0, "opacity:1;", None), (END_HOLD, "opacity:1;", None), (END, "opacity:0;", None),
                    (T - 0.02, "opacity:0;", None), (T, "opacity:1;", None)])
    for i, parts in enumerate(cmd):
        y = Y0 + i * LH
        o(f'<text x="{X0}" y="{y:.1f}" class="{cmdfade}">' + "".join(f'<tspan fill="{c}">{escape(s)}</tspan>' for s, c in parts) + "</text>")
    for i, parts in enumerate(cmd):
        y = Y0 + i * LH
        skip = 2 if i == 0 else 0  # prompt is already there
        chars = sum(len(s) for s, _ in parts) - skip
        t0, t1 = t_type[i]
        cx = X0 + skip * CW
        cls = typer(t0, t1, chars)
        # cover hides untyped text; cursor rides its left edge
        vis = appear(t_type[i - 1][1] if i else 0, 0.01) if i else ""
        o(f'<g class="{cls}"><rect x="{cx:.1f}" y="{y - 13:.1f}" width="560" height="{LH:.1f}" fill="{BG}"/>'
          f'<g class="{vis}"><rect x="{cx:.1f}" y="{y - 12:.1f}" width="{CW:.1f}" height="16" fill="{FG}" opacity=".85"/></g></g>')
    # grep output
    y = Y0 + 3 * LH
    o(f'<text x="{X0}" y="{y:.1f}" class="{appear(FETCH_DONE, 0.05)}"><tspan fill="{PURPLE}">kernel/sched/core.c</tspan>'
      f'<tspan fill="{DIM}">:</tspan><tspan fill="{GREEN}">5829</tspan><tspan fill="{DIM}">:</tspan>'
      f'<tspan fill="{P["hit"]}" font-weight="700">void sched_tick</tspan><tspan fill="{FG}">(void)</tspan></text>')
    sed_out = [
        "void sched_tick(void)",
        "{",
        "    int cpu = smp_processor_id();",
        "    struct rq *rq = cpu_rq(cpu);",
        "    /* scheduler accounting goes to the donor task */",
        "    struct task_struct *curr, *donor;",
        "    struct rq_flags rf;",
        "    unsigned long hw_pressure;",
        "    u64 resched_latency;",
    ]
    for j, line in enumerate(sed_out):
        y = Y0 + (4 + j) * LH
        col = P["comment"] if line.strip().startswith("/*") else P["code"]
        o(f'<text x="{X0}" y="{y:.1f}" fill="{col}" class="{appear(FETCH_DONE + 0.08 + j * 0.06, 0.05)}">{escape(line)}</text>')
    y = Y0 + 13 * LH
    blink = anim([(0, "opacity:0;", None), (FETCH_DONE + 0.8, "opacity:0;", None), (FETCH_DONE + 0.81, "opacity:1;", None)]
                 + [(FETCH_DONE + 1.4 + k * 1.1 + d, f"opacity:{v};", None) for k in range(9) for d, v in ((0, 1), (0.01, 0), (0.55, 0), (0.56, 1))
                    if FETCH_DONE + 1.4 + k * 1.1 + d < END_HOLD]
                 + [(END_HOLD, "opacity:1;", None), (END, "opacity:0;", None), (T, "opacity:0;", None)])
    o(f'<g class="{appear(FETCH_DONE + 0.8, 0.01)}"><text x="{X0}" y="{y:.1f}" fill="{GREEN}">$</text></g>'
      f'<rect class="{blink}" x="{X0 + 2 * CW:.1f}" y="{y - 12:.1f}" width="{CW:.1f}" height="16" fill="{FG}" opacity=".85"/>')
    o("</g>")

    # ---------- fetched-tree panel
    PX, PY, PW = 624, 78, 304
    window(PX, PY, PW, 328, "what crossed the wire")
    rows = [  # (indent, label, kind, time)   kind: root/dir/file/ghost/more
        (0, "torvalds/linux", "root", ENTER + 0.15),
        (1, "Documentation/", "ghost", 0),
        (1, "arch/", "ghost", 0),
        (1, "drivers/", "ghost", 0),
        (1, "include/", "ghost", 0),
        (1, "kernel/", "dir", ENTER + 0.45),
        (2, "bpf/", "ghost", 0),
        (2, "cgroup/", "ghost", 0),
        (2, "sched/", "dir", ENTER + 0.8),
        (3, "core.c", "hit", ENTER + 1.25),
        (3, "fair.c", "blob", ENTER + 1.15),
        (3, "… 40 more files", "blob", ENTER + 1.2),
        (2, "… 143 more", "ghost", 0),
        (1, "mm/", "ghost", 0),
        (1, "net/", "ghost", 0),
        (1, "… 34 more", "ghost", 0),
    ]
    TX, TY, TLH = PX + 20, PY + 52, 15.6
    o(f'<g font-family="{MONO}" font-size="12.5">')
    # connector lines
    for i, (ind, label, kind, t) in enumerate(rows):
        if ind == 0:
            continue
        y = TY + i * TLH
        gx = TX + (ind - 1) * 18 + 4
        o(f'<path d="M{gx} {y - 11}V{y - 4}H{gx + 8}" fill="none" stroke="{P["stroke"]}"/>')
    for i, (ind, label, kind, t) in enumerate(rows):
        y = TY + i * TLH
        x = TX + (ind * 18 if ind == 0 else (ind - 1) * 18 + 16)
        if kind == "ghost":
            o(f'<text x="{x}" y="{y:.1f}" fill="{GHOST}">{escape(label)}</text>')
        elif kind == "root":
            o(f'<text x="{x}" y="{y:.1f}" font-weight="700" fill="{FG}" class="{light(t, GHOST, FG)}">{escape(label)}</text>')
        elif kind == "dir":
            o(f'<text x="{x}" y="{y:.1f}" font-weight="700" fill="{BLUE}" class="{light(t, GHOST, BLUE)}">{escape(label)}</text>')
        elif kind == "blob":
            o(f'<text x="{x}" y="{y:.1f}" fill="{P["comment"]}" class="{flash(t, GHOST, GREEN, P["comment"])}">{escape(label)}</text>')
        elif kind == "hit":
            o(f'<rect x="{x - 5}" y="{y - 12.5:.1f}" width="{PW - (x - PX) - 14}" height="17" rx="4" fill="{GREEN}" fill-opacity=".14" stroke="{GREEN}" stroke-opacity=".5" class="{appear(FETCH_DONE, 0.3)}"/>')
            o(f'<text x="{x}" y="{y:.1f}" font-weight="700" fill="{GREEN}"{GLOW} class="{flash(t, GHOST, GREEN, GREEN)}">{escape(label)}</text>')
            o(f'<text x="{PX + PW - 26}" y="{y:.1f}" text-anchor="end" font-size="11" fill="{GREEN}" class="{appear(FETCH_DONE, 0.3)}">:5829</text>')
    o("</g>")
    o(f'<text x="{PX + PW / 2}" y="{PY + 314}" text-anchor="middle" font-family="{SANS}" font-size="12" fill="{DIM}" class="{appear(FETCH_DONE + 0.2)}">'
      f'<tspan fill="{FG}" font-weight="600">5</tspan> requests · <tspan fill="{FG}" font-weight="600">54</tspan> objects · <tspan fill="{GREEN}" font-weight="600">653 KB</tspan></text>')

    # ---------- comparison panel
    BX, BY, BW, BH = 32, 424, 896, 176
    o(f'<rect x="{BX}" y="{BY}" width="{BW}" height="{BH}" rx="11" fill="{P["panel"]}" fill-opacity="{P["panel_op"]}" stroke="{P["stroke"]}"/>')
    TRK_X, TRK_W = 232, 440
    CLONE_END = ENTER + 9.5
    rowsb = [
        ("DATA", BY + 30, [
            ("git clone --depth 1", "obar", 1.0, "291 MB", ORANGE, ENTER, CLONE_END),
            ("explorerepo", "gbar", 0.653 / 291, "653 KB", GREEN, ENTER, FETCH_DONE),
        ]),
        ("TIME", BY + 100, [
            ("git clone --depth 1", "obar", 1.0, "33.7 s", ORANGE, ENTER, CLONE_END),
            ("explorerepo", "gbar", 1.99 / 33.7, "2.0 s", GREEN, ENTER, FETCH_DONE),
        ]),
    ]
    for head, hy, bars in rowsb:
        o(f'<text x="{BX + 24}" y="{hy}" font-family="{SANS}" font-size="11" font-weight="700" letter-spacing="1.5" fill="{DIM}">{head}</text>')
        for k, (label, grad, frac, val, col, t0, t1) in enumerate(bars):
            y = hy + 12 + k * 24
            o(f'<text x="{TRK_X - 14}" y="{y + 10}" text-anchor="end" font-family="{MONO}" font-size="12.5" fill="{FG if grad == "gbar" else P["code"]}">{escape(label)}</text>')
            o(f'<rect x="{TRK_X}" y="{y}" width="{TRK_W}" height="13" rx="6.5" fill="{P["track"]}"/>')
            bw = max(TRK_W * frac, 4)
            o(f'<rect x="{TRK_X}" y="{y}" width="{bw:.1f}" height="13" rx="{min(6.5, bw / 2):.1f}" fill="url(#{grad})" class="{grow(t0, t1, "linear" if grad == "obar" else "cubic-bezier(.2,.7,.3,1)")}"/>')
            o(f'<text x="{TRK_X + max(bw, 4) + 10:.1f}" y="{y + 10.5}" font-family="{MONO}" font-size="12.5" font-weight="700" fill="{col}" class="{appear(t1, 0.2)}">{val}</text>')
    # callouts
    CX = BX + BW - 96
    o(f'<path d="M{CX - 84} {BY + 22}V{BY + BH - 22}" stroke="{P["stroke"]}"/>')
    for (big, small, cy) in (("445×", "less data", BY + 66), ("17×", "faster", BY + 136)):
        cls = appear(CLONE_END + 0.3, 0.4)
        o(f'<g class="{cls}"><text x="{CX}" y="{cy}" text-anchor="middle" font-family="{SANS}" font-size="32" font-weight="800" fill="{GREEN}"{GLOW}>{big}</text>'
          f'<text x="{CX}" y="{cy + 18}" text-anchor="middle" font-family="{SANS}" font-size="12.5" fill="{FG}">{small}</text></g>')
    o(f'<text x="{W / 2}" y="{H - 10}" text-anchor="middle" font-family="{SANS}" font-size="11" fill="{P["footer"]}">'
      f'measured 2026-11-06 on a cold cache: find and read sched_tick in torvalds/linux</text>')

    style = "<style>" + "".join(kf_rules) + "".join(css_rules) + \
        "text{white-space:pre}@media (prefers-reduced-motion:reduce){*{animation:none!important}}</style>"
    return (f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" xml:space="preserve" '
            f'role="img" aria-label="explorerepo greps and reads kernel/sched/core.c in torvalds/linux in 2 seconds and 653 KB, versus 33.7 seconds and 291 MB for a shallow git clone">'
            + style + "".join(out) + "</svg>\n")


def main():
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    for theme in ("dark", "light"):
        path = os.path.join(root, "docs", f"demo-{theme}.svg")
        with open(path, "w") as f:
            f.write(build(theme))
        print(path)


if __name__ == "__main__":
    main()
