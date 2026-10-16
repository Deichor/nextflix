"""Tables of a truth run: offset runs (acts) per subtitle and the scores of the served timings.

  report.py --sub <label> <lines.json> <served.vtt> [--sub ...] [--watched A B]

Prints markdown. Truth lines with conf high or medium count; errors are served start - truth start
(positive = the cue shows after the speech starts). --watched scores a stretch of the file on its own
(file seconds, e.g. what the viewer played).
"""
import json
import sys

import numpy as np

sys.path.append("/scripts")  # own modules after the standard library
from match import pair_served, parse_vtt  # noqa: E402

LEVELS = ("high", "medium")


def load(path):
    return [r for r in json.load(open(path))["lines"] if r.get("conf") in LEVELS and "truth" in r]


def segments(lines, jump=0.6, confirm=3):
    """Runs of (nearly) constant offset: a new run starts where `confirm` lines in a row sit more
    than `jump` s from the current run's median."""
    lines = sorted(lines, key=lambda r: r["truth"])
    runs, cur = [], []
    for k, r in enumerate(lines):
        if cur:
            med = float(np.median([x["offset"] for x in cur[-15:]]))
            nxt = lines[k:k + confirm]
            if len(nxt) == confirm and all(abs(x["offset"] - med) > jump for x in nxt) and \
                    np.ptp([x["offset"] for x in nxt]) < 2 * jump:
                runs.append(cur)
                cur = []
        cur.append(r)
    if cur:
        runs.append(cur)
    out = []
    for run in runs:
        offs = np.array([x["offset"] for x in run])
        t = np.array([x["raw_s"] for x in run])
        # drift inside the run (s per 1000 s of raw time): about 0 when the frame rate is right
        keep = np.abs(offs - np.median(offs)) <= 0.5
        slope = float(np.polyfit(t[keep], offs[keep], 1)[0] * 1000) if keep.sum() >= 5 and np.ptp(t[keep]) > 60 else float("nan")
        out.append({"from": run[0]["truth"], "to": run[-1]["truth"], "n": len(run),
                    "median": float(np.median(offs)), "p10": float(np.percentile(offs, 10)),
                    "p90": float(np.percentile(offs, 90)), "slope": slope,
                    "raw_from": run[0]["raw_s"], "raw_to": run[-1]["raw_s"]})
    return out


def score_rows(lines, served):
    pairs = pair_served(lines, served)
    return [(r["truth"], pairs[r["i"]]["s"] - r["truth"], r) for r in lines if r["i"] in pairs]


def summary(rows):
    e = np.array([x[1] for x in rows])
    if not len(e):
        return "n=0"
    return (f"n={len(e)}, median {np.median(e):+.2f} s, mean |err| {np.mean(np.abs(e)):.2f} s, "
            f"within 0.3 s {np.mean(np.abs(e) <= 0.3):.0%}, 0.5 s {np.mean(np.abs(e) <= 0.5):.0%}, "
            f"1 s {np.mean(np.abs(e) <= 1.0):.0%}")


def main():
    a = sys.argv
    subs = []
    for k, x in enumerate(a):
        if x == "--sub":
            lines = load(a[k + 2])
            subs.append({"label": a[k + 1], "lines": lines, "rows": score_rows(lines, parse_vtt(a[k + 3]))})
    watched = None
    if "--watched" in a:
        k = a.index("--watched")
        watched = (float(a[k + 1]), float(a[k + 2]))
    for s in subs:
        errs = {x[2]["i"]: x[1] for x in s["rows"]}
        sets = [(s["label"], s["lines"])]
        anchored = [r for r in s["lines"] if r.get("en_line_at")]
        if anchored:
            sets.append((f"{s['label']} (only lines starting where an English line starts)", anchored))
        for name, ls in sets:
            print(f"\n### {name}: offset runs (truth - raw start)\n")
            print("| file s from | to | raw from | raw to | lines | median | p10 | p90 | drift s/1000 s | served err median | served <=0.5 s |")
            print("|---|---|---|---|---|---|---|---|---|---|---|")
            for g in segments(ls):
                e = np.array([errs[r["i"]] for r in ls if g["from"] <= r["truth"] <= g["to"] and r["i"] in errs])
                se = f"{np.median(e):+.2f} | {np.mean(np.abs(e) <= 0.5):.0%}" if len(e) else " | "
                print(f"| {g['from']:.1f} | {g['to']:.1f} | {g['raw_from']:.1f} | {g['raw_to']:.1f} | {g['n']} | "
                      f"{g['median']:+.2f} | {g['p10']:+.2f} | {g['p90']:+.2f} | {g['slope']:+.2f} | {se} |")
    print("\n### Served timing scores (served start - truth start)\n")
    for s in subs:
        rows = s["rows"]
        print(f"- {s['label']}, whole file: {summary(rows)}")
        if watched:
            w = [x for x in rows if watched[0] <= x[0] < watched[1]]
            print(f"- {s['label']}, {watched[0]:.0f}-{watched[1]:.0f} s: {summary(w)}")
        ref = [(t, e + r["truth"] - r["ref_s"], r) for (t, e, r) in rows if "ref_s" in r]
        if ref:
            print(f"- {s['label']} against the file's own track (lines starting at the same word): {summary(ref)}")
        best = []  # the best any piecewise constant timing can do: each run at its median offset
        for g in segments(s["lines"]):
            best += [(r["truth"], g["median"] - r["offset"], r) for r in s["lines"] if g["from"] <= r["truth"] <= g["to"]]
        print(f"- {s['label']}, best piecewise constant timing: {summary(best)}")
    print("\n### Per minute (file time of the truth)\n")
    print("| min | " + " | ".join(f"{s['label']} n | offset | served err | <=0.5 s" for s in subs) + " |")
    print("|---|" + "---|---|---|---|" * len(subs))
    for m in sorted({int(t // 60) for s in subs for t, _, _ in s["rows"]}):
        cells = []
        for s in subs:
            sel = [x for x in s["rows"] if int(x[0] // 60) == m]
            if sel:
                e = np.array([x[1] for x in sel])
                o = np.array([x[2]["offset"] for x in sel])
                cells.append(f"{len(e)} | {np.median(o):+.2f} | {np.median(e):+.2f} | {np.mean(np.abs(e) <= 0.5):.0%}")
            else:
                cells.append("0 | | | ")
        print(f"| {m} | " + " | ".join(cells) + " |")
    for s in subs:
        sel = [x for x in s["rows"] if (not watched or watched[0] <= x[0] < watched[1]) and abs(x[1]) > 1.0]
        where = f"{watched[0]:.0f}-{watched[1]:.0f} s" if watched else "the file"
        print(f"\n### {s['label']}: served lines more than 1 s off in {where}\n")
        print(f"{len(sel)} lines: " + ", ".join(f"{t:.0f}s {e:+.1f}" for t, e, _ in sel[:80]))


if __name__ == "__main__":
    main()
