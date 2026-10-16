"""Text truth for a subtitle in another language: its machine translation (mt.py) matched to the
English transcript, line by line, in a window around where its fit puts it.

  mtmatch.py <aligned.json> <mt.json> <out.json> --fit <fit.json> [--window S]

Per cue: local alignment (Smith-Waterman) of the translation's words to the spoken words within
+-S s (default 3) of the fitted cue; content words weigh more than function words; a second,
non-overlapping alignment in the window measures how unique the match is (margin). Truth = start of
the first matched spoken word (or the word right before it when the translation's first word is
unmatched and that word starts a phrase). Only matches that start with the translation's first or
second word count (anchored): a match of the line's second half says nothing about its start.
conf: high (anchored, matched weight >= 3, margin >= 1.5), medium (anchored, >= 2, >= 0.75), low.
The window is the only use of timing, so the truth does not depend on the line's own timing.
Lines: i, raw_s, raw_e, text, en, truth, offset, conf, matched, first_pos, ext, score, margin.
"""
import json
import sys
from bisect import bisect_left

import numpy as np

sys.path.append("/scripts")  # own modules after the standard library
from match import load_fit, load_words, map_fit, similarity_table, tokens  # noqa: E402

STOP = set("""a an the and or but if then so of to in on at by for with from as is are was were be been being am
i you he she it we they me him her us them my your his its our their this that these those there here what
which who whom whose when where why how not no do does did done have has had having will would shall should
can could may might must just very too also oh uh um well yeah yes okay ok all any some""".split())


def weight(tok):
    return 0.5 if tok in STOP else 1.5


def local_align(et, ew, sim, cols, mask=None):
    """Best local alignment of translation tokens et (weights ew) to spoken words cols."""
    n, m = len(et), len(cols)
    H = np.zeros((n + 1, m + 1))
    P = np.zeros((n + 1, m + 1), dtype=np.int8)  # 0 stop, 1 diag, 2 up (token unmatched), 3 left (word skipped)
    for i in range(1, n + 1):
        for j in range(1, m + 1):
            v = 0.0
            if mask is None or not mask[j - 1]:
                v = sim.get((et[i - 1], cols[j - 1]), 0.0)
            best, p = 0.0, 0
            if v > 0:
                d = H[i - 1, j - 1] + ew[i - 1] * v
                if d > best:
                    best, p = d, 1
            u = H[i - 1, j] - 0.25 * ew[i - 1]
            if u > best:
                best, p = u, 2
            le = H[i, j - 1] - 0.2
            if le > best:
                best, p = le, 3
            H[i, j], P[i, j] = best, p
    i, j = (int(x) for x in np.unravel_index(int(np.argmax(H)), H.shape))
    score = float(H[i, j])
    pairs = []
    while i > 0 and j > 0 and P[i, j] != 0:
        if P[i, j] == 1:
            pairs.append((i - 1, j - 1))
            i, j = i - 1, j - 1
        elif P[i, j] == 2:
            i -= 1
        else:
            j -= 1
    return score, pairs[::-1]


def main():
    aligned, mtp, out = sys.argv[1], sys.argv[2], sys.argv[3]
    fit = load_fit(sys.argv[sys.argv.index("--fit") + 1])
    W = float(sys.argv[sys.argv.index("--window") + 1]) if "--window" in sys.argv else 3.0
    words = load_words(aligned)
    times = [w["s"] for w in words]
    cues = json.load(open(mtp))
    for c in cues:
        c["toks"] = tokens(c["en"])
    sim = similarity_table({t for c in cues for t in c["toks"]}, {w["tok"] for w in words})
    res = []
    prev_last = -1
    for c in sorted(cues, key=lambda x: x["s"]):
        r = {"i": c["i"], "raw_s": c["s"], "raw_e": c["e"], "text": c["text"], "en": c["en"]}
        et = c["toks"]
        if not et:
            r.update(conf="none", why="nothing to match")
            res.append(r)
            continue
        ew = [weight(t) for t in et]
        a = bisect_left(times, map_fit(fit, c["s"]) - W)
        b = bisect_left(times, map_fit(fit, c["e"]) + W)
        cols = [words[k]["tok"] for k in range(a, b)]
        if not cols:
            r.update(conf="none", why="no speech in the window")
            res.append(r)
            continue
        score, pairs = local_align(et, ew, sim, cols)
        if not pairs:
            r.update(conf="none", why="no word matched")
            res.append(r)
            continue
        mask = np.zeros(len(cols), dtype=bool)
        lo, hi = pairs[0][1], pairs[-1][1]
        mask[max(0, lo - 2):hi + 3] = True
        score2, _ = local_align(et, ew, sim, cols, mask)
        matched_w = sum(ew[i] * sim.get((et[i], cols[j]), 0.0) for i, j in pairs)
        first_pos = pairs[0][0]
        m = a + pairs[0][1]
        k = m
        # One unmatched leading word of the translation ("Well, ..."): the spoken word right before the
        # match, when it starts a phrase there and no earlier line took it
        if first_pos == 1 and m - 1 > prev_last and m - 1 >= a and words[m]["s"] - words[m - 1]["e"] < 0.3 and \
                (m - 2 < 0 or words[m - 1]["s"] - words[m - 2]["e"] >= 0.3 or m - 2 <= prev_last):
            k = m - 1
        while k > 0 and not words[k]["first"] and words[k - 1]["s"] == words[k]["s"]:
            k -= 1
        margin = score - score2
        # Only a match that starts with the translation's first words tells where the line starts
        anchored = first_pos <= 1
        if not anchored:
            conf, why = "low", f"match starts at word {first_pos + 1} of the translation"
        elif matched_w >= 3.0 and margin >= 1.5:
            conf, why = "high", ""
        elif matched_w >= 2.0 and margin >= 0.75:
            conf, why = "medium", ""
        else:
            conf, why = "low", "weak or ambiguous match"
        r.update(truth=round(words[k]["s"], 3), offset=round(words[k]["s"] - c["s"], 3), conf=conf,
                 matched=" ".join(words[a + j]["raw"] for _, j in pairs), first_pos=first_pos, ext=m - k,
                 score=round(score, 2), margin=round(margin, 2))
        if why:
            r["why"] = why
        if matched_w >= 2.0 and margin >= 0.75:
            prev_last = a + pairs[-1][1]
        res.append(r)
    from collections import Counter
    summary = {"cues": len(res), "conf": dict(Counter(r["conf"] for r in res))}
    json.dump({"subtitle": mtp, "method": "mt", "summary": summary, "lines": res}, open(out, "w"),
              ensure_ascii=False, indent=0)
    print(json.dumps(summary))


if __name__ == "__main__":
    main()
