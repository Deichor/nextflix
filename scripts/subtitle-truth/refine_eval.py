"""Served (refined) cues vs the same fit without line refinement, both against a truth.

  refine_eval.py <lines.json> <raw.vtt> <served.vtt> <fit.json> [--label X] [--conf high,medium]
                 [--own own_lines.json] [--agree other_lines.json]

fit-only start = the stored fit applied to the raw cue (sync_align.go mapTime: u = start * scale,
plus the offset of the segment u falls in). A line counts as moved when served and fit-only starts
differ by 0.1 s or more. Errors are start - truth (positive = late).
--own: the file's own track matched to the speech (match.py lines); for moved lines the own line the
cue was moved onto (raw start within 0.02 s of the served start) and that line's own error are shown.
--agree: only lines whose truth agrees (0.15 s) with a second truth file for the same subtitle.
"""
import json
import sys

import numpy as np

sys.path.append("/scripts")  # own modules after the standard library
from match import load_fit, map_fit, pair_served, parse_vtt  # noqa: E402


def st(e):
    e = np.asarray(e, dtype=float)
    if not len(e):
        return "n=0"
    return (f"n={len(e)}, median {np.median(e):+.2f}, p10 {np.percentile(e, 10):+.2f}, p90 {np.percentile(e, 90):+.2f}, "
            f"within 0.15 / 0.3 / 0.5 s: {np.mean(np.abs(e) <= 0.15):.0%} / {np.mean(np.abs(e) <= 0.3):.0%} / "
            f"{np.mean(np.abs(e) <= 0.5):.0%}")


def main():
    a = sys.argv
    lines = json.load(open(a[1]))["lines"]
    raw, served, fit = parse_vtt(a[2]), parse_vtt(a[3]), load_fit(a[4])
    label = a[a.index("--label") + 1] if "--label" in a else a[1]
    levels = tuple(a[a.index("--conf") + 1].split(",")) if "--conf" in a else ("high", "medium")
    own = json.load(open(a[a.index("--own") + 1]))["lines"] if "--own" in a else None
    agree = None
    if "--agree" in a:
        agree = {r["i"]: r["truth"] for r in json.load(open(a[a.index("--agree") + 1]))["lines"]
                 if r.get("conf") in levels and "truth" in r}
    rawd = {c["i"]: c for c in raw}
    pairs = pair_served([{"i": c["i"], "text": c["text"]} for c in raw], served)
    rows = []
    for r in lines:
        if r.get("conf") not in levels or "truth" not in r or r["i"] not in pairs:
            continue
        if agree is not None and (r["i"] not in agree or abs(agree[r["i"]] - r["truth"]) > 0.15):
            continue
        fo = map_fit(fit, rawd[r["i"]]["s"])
        sv = pairs[r["i"]]["s"]
        rows.append({"i": r["i"], "truth": r["truth"], "fit": fo - r["truth"], "served": sv - r["truth"],
                     "moved": abs(sv - fo) >= 0.1, "sv": sv, "text": r["text"]})
    moved_all = sum(1 for c in raw if c["i"] in pairs and abs(pairs[c["i"]]["s"] - map_fit(fit, c["s"])) >= 0.1)
    print(f"## {label}")
    print(f"cues {len(raw)}, moved by the refinement (>= 0.1 s) {moved_all}; lines with truth {len(rows)}, "
          f"of them moved {sum(x['moved'] for x in rows)}")
    print(f"- all lines, fit only: {st([x['fit'] for x in rows])}")
    print(f"- all lines, served:   {st([x['served'] for x in rows])}")
    mv = [x for x in rows if x["moved"]]
    print(f"- moved lines, fit only: {st([x['fit'] for x in mv])}")
    print(f"- moved lines, served:   {st([x['served'] for x in mv])}")
    if mv:
        better = sum(abs(x["served"]) < abs(x["fit"]) - 0.05 for x in mv)
        worse = sum(abs(x["served"]) > abs(x["fit"]) + 0.05 for x in mv)
        gain = np.mean([abs(x["fit"]) - abs(x["served"]) for x in mv])
        print(f"- moved lines: refinement closer to the truth {better}, farther {worse}, "
              f"same within 0.05 s {len(mv) - better - worse}; mean |error| change {-gain:+.2f} s")
    if own and mv:
        ol = [o for o in own if o.get("conf") in ("high", "medium") and "truth" in o]
        errs = []
        for x in mv:
            hit = [o for o in ol if abs(o["raw_s"] - x["sv"]) <= 0.02]
            if hit:
                errs.append(hit[0]["raw_s"] - hit[0]["truth"])
        print(f"- the own lines moved onto (found for {len(errs)} of {len(mv)}): own start - own line's speech start: {st(errs)}")


if __name__ == "__main__":
    main()
