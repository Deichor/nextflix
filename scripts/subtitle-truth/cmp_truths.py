"""Agreement of two truth files for the same subtitle (by cue index), lines conf high/medium in both.

  cmp_truths.py <a.json> <b.json> [--list N]
"""
import json
import sys

import numpy as np

a = {r["i"]: r for r in json.load(open(sys.argv[1]))["lines"] if r.get("conf") in ("high", "medium") and "truth" in r}
b = {r["i"]: r for r in json.load(open(sys.argv[2]))["lines"] if r.get("conf") in ("high", "medium") and "truth" in r}
both = sorted(set(a) & set(b))
d = np.array([a[i]["truth"] - b[i]["truth"] for i in both])
print(json.dumps({"a": len(a), "b": len(b), "both": len(both),
                  **{f"le_{x}": round(float(np.mean(np.abs(d) <= x)), 3) for x in (0.05, 0.1, 0.3, 0.5, 1.0)},
                  "median": round(float(np.median(d)), 3) if len(d) else None}))
n = int(sys.argv[sys.argv.index("--list") + 1]) if "--list" in sys.argv else 0
for i in [i for i in both if abs(a[i]["truth"] - b[i]["truth"]) > 0.3][:n]:
    print(f'{i:4d} a {a[i]["truth"]:8.2f} b {b[i]["truth"]:8.2f} {a[i]["conf"][0]}{b[i]["conf"][0]} | {a[i]["text"][:40]!r} | {a[i].get("en", "")[:40]!r} | {a[i].get("matched", "")[:40]!r}')
