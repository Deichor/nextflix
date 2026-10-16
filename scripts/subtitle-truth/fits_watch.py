"""Score the timings a viewer actually got: fits from the server log, each over the stretch watched
while it was served.

  fits_watch.py <lines.json> <raw.vtt> <spec.json>

spec.json: [{"label": ..., "scale": 1.001001, "segments": [[from, offset], ...], "watched": [A, B]}, ...]
(segments as logged: from on the scaled clock, the first one from 0). Cues are timed like the
server's applyFit; errors are served start - truth start for truth lines (conf high/medium) whose
truth lies in the watched stretch.
"""
import json
import sys

import numpy as np

sys.path.append("/scripts")  # own modules after the standard library
from match import parse_vtt  # noqa: E402


def main():
    lines = {r["i"]: r for r in json.load(open(sys.argv[1]))["lines"] if r.get("conf") in ("high", "medium") and "truth" in r}
    raw = parse_vtt(sys.argv[2])
    for f in json.load(open(sys.argv[3])):
        a, b = f["watched"]
        errs = []
        for c in raw:
            r = lines.get(c["i"])
            if not r or not (a <= r["truth"] < b):
                continue
            u = c["s"] * f["scale"]
            off = 0.0
            for k, (frm, o) in enumerate(f["segments"]):
                if k == 0 or u >= frm:
                    off = o
            errs.append((r["truth"], u + off - r["truth"]))
        e = np.array([x[1] for x in errs])
        if not len(e):
            print(f'{f["label"]}: no lines')
            continue
        bad = [x for x in errs if abs(x[1]) > 1.0]
        stretch = f" (> 1 s: {len(bad)} lines, {bad[0][0]:.0f}-{bad[-1][0]:.0f} s, median {np.median([x[1] for x in bad]):+.2f})" if bad else ""
        print(f'{f["label"]}, watched {a:.0f}-{b:.0f} s: n={len(e)}, median {np.median(e):+.2f}, '
              f'within 0.3 s {np.mean(np.abs(e) <= 0.3):.0%}, 0.5 s {np.mean(np.abs(e) <= 0.5):.0%}, '
              f'1 s {np.mean(np.abs(e) <= 1.0):.0%}{stretch}')


if __name__ == "__main__":
    main()
