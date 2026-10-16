"""Independent text check of a structural (other-language) truth through names and numbers.

  names_check.py <tr_lines.json> <aligned.json>

For every timed line with a proper name (a capitalized word that is not the first word of a
sentence) or a number, look for it in the transcript words spoken from the line's truth start to
its end (+1 s). A shifted control (truth +-4 s) shows what chance agreement looks like.
"""
import json
import re
import sys
from bisect import bisect_left

LEVELS = ("high", "medium")


def name_tokens(text):
    out = set()
    for line in text.split("\n"):
        line = re.sub(r"^\s*-\s*", "", line)
        for sent in re.split(r"(?<=[.?!])\s+", line):
            ws = sent.split()
            for k, w in enumerate(ws):
                core = re.sub(r"['’].*$", "", w)  # Turkish suffixes after an apostrophe: Jane'in -> Jane
                core = re.sub(r"[^\wÇĞİÖŞÜçğıöşü]", "", core)
                if re.fullmatch(r"\d+", core):
                    out.add(core)
                elif k > 0 and len(core) >= 3 and core[0].isupper() and not core.isupper():
                    out.add(core.lower())
    return out


def main():
    lines = json.load(open(sys.argv[1]))["lines"]
    words = json.load(open(sys.argv[2]))["words"]
    times = [w["s"] for w in words]
    toks = [re.sub(r"[^\w]", "", w["w"].lower()) for w in words]

    from rapidfuzz import fuzz

    def spoken(a, b):
        i, j = bisect_left(times, a), bisect_left(times, b)
        return set(toks[i:j])

    def heard(n, a, b):
        return any(t == n or t.startswith(n) or (len(n) >= 4 and fuzz.ratio(n, t) >= 80) for t in spoken(a, b))
    res = {"lines": 0, "found": 0, "control": 0}
    shown = 0
    for r in lines:
        if r.get("conf") not in LEVELS or "truth" not in r:
            continue
        names = name_tokens(r["text"])
        if not names:
            continue
        dur = max(1.5, r["raw_e"] - r["raw_s"])
        a, b = r["truth"] - 0.3, r["truth"] + dur + 1.0
        hit = any(heard(n, a, b) for n in names)
        ctl = any(heard(n, a + d, b + d) for d in (-4.0, 4.0) for n in names)
        res["lines"] += 1
        res["found"] += hit
        res["control"] += bool(ctl)
        if not hit and shown < 12:
            shown += 1
            print(f'  not found: {r["truth"]:.1f} {sorted(names)} | {r["text"][:60]!r}')
    print(json.dumps(res))


if __name__ == "__main__":
    main()
