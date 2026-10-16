"""Subtitle lines vs ASR word timings: ground truth, transfer and scores.

Runs in the ASR container (pyrun.sh). Times are seconds on the file's timeline.

  match.py lines <aligned.json> <subs.vtt> <out.json> [--fit fit.json] [--band S]
      Text truth: every cue of a subtitle (raw, as the provider gave it) is matched to the
      transcript by one monotonic word alignment (banded by time around the fit, or around an
      offset estimated from distinctive words), truth = start of the line's first spoken word.
  match.py pgsvtt <pgs_cues.json> <img_dir> <out.vtt>
      PGS cues (pgs.py) with their OCR text (ocr.sh) as WebVTT.
  match.py sametiming <a.vtt> <b.vtt> [--check]
      Do two subtitles share their raw cue timing (80 % of starts within 0.1 s at one offset)?
      --check: exit 0 when shared, 3 when not.
  match.py transfer <en_lines.json> <other.vtt> <out.json>
      Shared timing: every cue takes the truth of the timed subtitle's line that starts with it.
  match.py struct <aligned.json> <subs.vtt> <out.json> [--fit fit.json] [--lines en_lines.json] [--ref ref_lines.json]
      Structural truth for a subtitle in another language: frame rate and piecewise constant offsets
      (Viterbi over offsets, per cue) onto the transcript's phrase onsets, then each cue snapped to
      the onset nearest to it.
  match.py validate <lines.json> [--min high|medium] [--check]
      Own-track check: the matched lines' raw starts (a track made for the file) vs the ASR starts.
      --check: exit 0 when the track is timed to the file, 3 when not.
  match.py words <aligned.json> <words.json>
      Compact word list.
  match.py score <lines.json> <served.vtt> [--range A B] [--label X] [--use truth|ref_s]
      Served cue starts vs the truth (cues paired by index and text with the raw subtitle).

--ref (lines, struct): the lines of a track timed to the file (e.g. its own image track, OCR'd); a
line starting at the same spoken word gets ref_s, and a word the reference puts elsewhere marks the
line low.
"""
import json
import re
import sys
from bisect import bisect_left

import numpy as np

# ---------------------------------------------------------------- parsing and normalizing


def ts(s):
    p = s.replace(",", ".").split(":")
    return float(p[-1]) + (int(p[-2]) * 60 if len(p) >= 2 else 0) + (int(p[-3]) * 3600 if len(p) >= 3 else 0)


def parse_vtt(path):
    text = open(path, encoding="utf-8", errors="replace").read().replace("\r", "")
    cues = []
    for block in re.split(r"\n\s*\n", text):
        lines = [x for x in block.split("\n") if x.strip()]
        for k, ln in enumerate(lines):
            m = re.match(r"\s*([\d:.,]+)\s*-->\s*([\d:.,]+)", ln)
            if m:
                body = "\n".join(lines[k + 1:])
                cues.append({"i": len(cues), "s": round(ts(m.group(1)), 3), "e": round(ts(m.group(2)), 3), "text": body})
                break
    return cues


ONES = "zero one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen " \
       "seventeen eighteen nineteen".split()
TENS = "_ _ twenty thirty forty fifty sixty seventy eighty ninety".split()


def num_words(n):
    if n < 20:
        return ONES[n]
    if n < 100:
        return TENS[n // 10] + ("" if n % 10 == 0 else " " + ONES[n % 10])
    if n < 1000:
        return ONES[n // 100] + " hundred" + ("" if n % 100 == 0 else " " + num_words(n % 100))
    if n < 1000000:
        return num_words(n // 1000) + " thousand" + ("" if n % 1000 == 0 else " " + num_words(n % 1000))
    return " ".join(ONES[int(c)] for c in str(n))


def tokens(text):
    t = text.lower().replace("’", "'").replace("‘", "'").replace("`", "'").replace("´", "'")
    t = re.sub(r"<[^>]*>|\{[^}]*\}", " ", t)
    t = re.sub(r"\[[^\]]*\]|\([^)]*\)|♪[^\n]*", " ", t)
    t = re.sub(r"(?m)^\s*[a-z][a-z .#0-9]{0,20}:\s", " ", t)  # SDH speaker labels
    t = re.sub(r"\d+", lambda m: " " + (num_words(int(m.group())) if len(m.group()) < 7 else m.group()) + " ", t)
    t = re.sub(r"[-–—/…]|\.\.\.", " ", t)
    out = []
    for w in re.findall(r"[a-z0-9']+", t):
        w = w.strip("'")
        if w:
            out.append(w)
    return out


def similarity_table(svocab, tvocab):
    """sim[(s, t)] for word pairs that are equal or close (rapidfuzz ratio >= 80)."""
    from rapidfuzz import fuzz, process
    sim = {}
    tv = sorted(tvocab)
    tset = set(tv)
    for s in svocab:
        if s in tset:
            sim[(s, s)] = 1.0
        if len(s) >= 3:
            for t, score, _ in process.extract(s, tv, scorer=fuzz.ratio, score_cutoff=80, limit=None):
                if t != s and len(t) >= 3:
                    sim[(s, t)] = max(sim.get((s, t), 0), 0.75 * score / 100)
        for t in (s + "s", s + "'s", s[:-1] if s.endswith("s") else None, s + "in'", s + "ing"):
            if t and t in tset and t != s:
                sim[(s, t)] = max(sim.get((s, t), 0), 0.8)
    for a, b in (("ok", "okay"), ("okay", "ok"), ("gonna", "going"), ("wanna", "want"), ("yeah", "yes"),
                 ("yes", "yeah"), ("hey", "hi"), ("mm", "mmm"), ("uh", "um"), ("um", "uh"), ("mr", "mister"),
                 ("mrs", "missus"), ("till", "until"), ("cause", "because"), ("'cause", "because")):
        if a in svocab and b in tset:
            sim[(a, b)] = max(sim.get((a, b), 0), 0.8)
    return sim


# ---------------------------------------------------------------- the transcript

def load_words(path):
    d = json.load(open(path))
    ws = []
    for w in d["words"]:
        toks = tokens(w["w"])
        for k, tok in enumerate(toks):
            # a word that normalizes to several tokens ("45" -> forty five) shares its times
            ws.append({"tok": tok, "s": w["s"], "e": w["e"], "ws": w["ws"], "on": w.get("on"), "cs": w.get("cs"),
                       "p": w.get("p"), "raw": w["w"], "first": k == 0})
    return ws


def phrase_onsets(words, gap=0.25):
    """Word starts that begin a phrase: after a pause or a sentence end."""
    out = []
    prev = None
    for w in words:
        if not w["first"]:
            continue
        if prev is None or w["s"] - prev["e"] >= gap or re.search(r"[.?!]$", prev["raw"]):
            out.append(w["s"])
        prev = w
    return np.array(sorted(set(out)))


# ---------------------------------------------------------------- expectations

def load_fit(path):
    f = json.load(open(path))
    return f.get("fit", f)


def map_fit(fit, t):
    u = t * fit["scale"]
    off = 0.0
    for k, s in enumerate(fit["segments"]):
        if k == 0 or u >= s["from"]:
            off = s["offset"]
    return u + off


def anchor_offsets(cues, words, maxoff=90.0):
    """Offset estimate per cue from distinctive words (rare in both), median over +-150 s."""
    from collections import Counter
    sc = Counter(t for c in cues for t in c["toks"])
    tc = Counter(w["tok"] for w in words)
    tpos = {}
    for w in words:
        tpos.setdefault(w["tok"], []).append(w["s"])
    pts = []  # (subtitle time, offset)
    for c in cues:
        for tok in c["toks"]:
            if len(tok) >= 5 and sc[tok] <= 3 and 0 < tc.get(tok, 0) <= 3:
                for t in tpos[tok]:
                    if abs(t - c["s"]) <= maxoff:
                        pts.append((c["s"], t - c["s"]))
    pts.sort()
    est = []
    times = [p[0] for p in pts]
    for c in cues:
        a, b = bisect_left(times, c["s"] - 150), bisect_left(times, c["s"] + 150)
        offs = np.array([p[1] for p in pts[a:b]])
        if len(offs) < 3:
            est.append(None)
            continue
        # densest 2 s bin, then the median inside it
        hist = np.round(offs / 1.0)
        vals, counts = np.unique(hist, return_counts=True)
        best = vals[np.argmax(counts)]
        sel = offs[np.abs(offs - best) <= 1.5]
        est.append(float(np.median(sel)))
    # fill gaps
    last = next((e for e in est if e is not None), 0.0)
    for k in range(len(est)):
        if est[k] is None:
            est[k] = last
        last = est[k]
    return est


# ---------------------------------------------------------------- monotonic alignment

GAP_S, GAP_T = -0.5, -0.25


def align(stoks, sexp, words, sim, band):
    """Banded (by time) global monotonic word alignment. Returns {s index: (t index, sim)}."""
    n, m = len(stoks), len(words)
    ttimes = np.array([w["s"] for w in words])
    ttok = [w["tok"] for w in words]
    tindex = {}
    for j, t in enumerate(ttok):
        tindex.setdefault(t, []).append(j)
    simto = {}
    for (a, b), v in sim.items():
        simto.setdefault(a, []).append((b, v))
    NEG = -1e9
    H_prev = np.zeros(m + 1)  # column 0 = before any T word; row -1: free leading T gaps
    ptr = np.zeros((n, m + 1), dtype=np.int8)
    jidx = np.arange(m + 1, dtype=np.float64)
    for i in range(n):
        M = np.full(m + 1, NEG)
        lo, hi = bisect_left(ttimes, sexp[i] - band), bisect_left(ttimes, sexp[i] + band)
        for (tb, v) in simto.get(stoks[i], ()):
            pos = tindex.get(tb, ())
            for j in pos[bisect_left(pos, lo):bisect_left(pos, hi)]:
                dt = abs(ttimes[j] - sexp[i])
                M[j + 1] = max(M[j + 1], 2.0 * v - 0.04 * max(0.0, dt - 3.0))
        diag = np.full(m + 1, NEG)
        diag[1:] = H_prev[:-1] + M[1:]
        up = H_prev + GAP_S
        A = np.maximum(diag, up)
        src = np.where(diag >= up, 0, 1).astype(np.int8)
        B = A - GAP_T * jidx
        C = np.maximum.accumulate(B)
        H = C + GAP_T * jidx
        left = H > A + 1e-9
        src[left] = 2
        ptr[i] = src
        H_prev = H
    j = int(np.argmax(H_prev))
    i = n - 1
    pairs = {}
    while i >= 0 and j >= 0:
        p = ptr[i, j]
        if p == 0 and j > 0:
            pairs[i] = j - 1
            i, j = i - 1, j - 1
        elif p == 1 or j == 0:
            i -= 1
        else:
            j -= 1
    out = {}
    for i, j in pairs.items():
        v = sim.get((stoks[i], ttok[j]), 0)
        if v > 0:
            out[i] = (j, v)
    return out


def match_lines(words, cues, sim, expect, band):
    stoks, sexp, owner = [], [], []
    for c in cues:
        n = len(c["toks"])
        for k, tok in enumerate(c["toks"]):
            stoks.append(tok)
            frac = k / max(n, 1)
            sexp.append(expect(c["s"] + frac * max(0.0, c["e"] - c["s"])))
            owner.append(c["i"])
    pairs = align(stoks, sexp, words, sim, band)
    # per cue
    first_idx = {}
    for k, ci in enumerate(owner):
        first_idx.setdefault(ci, k)
    res = []
    for c in cues:
        n = len(c["toks"])
        if n == 0:
            res.append({"i": c["i"], "raw_s": c["s"], "raw_e": c["e"], "text": c["text"], "conf": "none", "why": "no words"})
            continue
        k0 = first_idx[c["i"]]
        m = [(k - k0, pairs[k][0], pairs[k][1]) for k in range(k0, k0 + n) if k in pairs]
        r = {"i": c["i"], "raw_s": c["s"], "raw_e": c["e"], "text": c["text"], "ntok": n, "nmatch": len(m)}
        if not m:
            r.update(conf="none", why="no word matched")
            res.append(r)
            continue
        pos, j, v = m[0]
        span = m[-1][1] - m[0][1] + 1
        r["first_pos"] = pos
        r["span"] = span
        r["exact"] = sum(1 for x in m if x[2] >= 1.0)
        tj, how = j, "first word"
        if pos > 0:
            # unmatched leading subtitle words: spoken words right before the first match that no
            # earlier line took may be them
            prev_j = max([x["j_last"] for x in res if "j_last" in x] or [-1])
            back = j
            while back - 1 > prev_j and j - (back - 1) <= pos + 1 and words[back]["s"] - words[back - 1]["e"] < 0.5:
                back -= 1
            if back < j:
                tj, how = back, f"{j - back} unmatched spoken word(s) before the first match"
            else:
                how = f"first {pos} subtitle word(s) not heard"
        else:
            # spoken words the subtitle left out right before its first word ("So how you doing?"
            # for "How are you doing?"), when they start a phrase: noted, not part of the line
            prev_j = max([x["j_last"] for x in res if "j_last" in x] or [-1])
            back = j
            while back - 1 > prev_j and j - (back - 1) <= 2 and words[back]["s"] - words[back - 1]["e"] < 0.3:
                back -= 1
            if back < j and (back == 0 or words[back]["s"] - words[back - 1]["e"] >= 0.3):
                r["lead_words"] = " ".join(words[q]["raw"] for q in range(back, j))
                r["lead_s"] = round(words[back]["s"], 3)
        while tj > 0 and not words[tj]["first"] and words[tj - 1]["s"] == words[tj]["s"]:
            tj -= 1  # a word split into several tokens starts with its first token
        r["j_last"] = m[-1][1]
        r["truth"] = round(words[tj]["s"], 3)
        r["truth_ws"] = round(words[tj]["ws"], 3)
        r["truth_on"] = words[tj]["on"]
        r["word"] = words[tj]["raw"]
        r["how"] = how
        compact = span <= 2 * n + 3
        ratio = len(m) / n
        # whisper sometimes puts a line's first words seconds before the rest (over music); the
        # aligner then keeps them there: a start in a smaller block than the rest is not trusted
        blocks = [[m[0]]]
        for x in m[1:]:
            if words[x[1]]["s"] - words[blocks[-1][-1][1]]["e"] > 1.5:
                blocks.append([])
            blocks[-1].append(x)
        detached = len(blocks) > 1 and len(blocks[0]) < len(m) - len(blocks[0])
        if detached:
            r["conf"], r["why"] = "low", "first word far from the rest of the line"
        elif pos == 0 and compact and (len(m) >= 2 or (n == 1 and v >= 1.0)) and ratio >= 0.5:
            r["conf"] = "high"
        elif pos <= 2 and compact and len(m) >= 2 and ratio >= 0.4:
            r["conf"] = "medium"
        elif pos == 0 and v >= 1.0 and n <= 3:
            r["conf"] = "medium"
        else:
            r["conf"] = "low"
        res.append(r)
    # consistency: offset vs the local median of confident neighbours
    good = [r for r in res if r.get("conf") == "high"]
    gt = np.array([r["raw_s"] for r in good])
    go = np.array([r["truth"] - r["raw_s"] for r in good])
    for r in res:
        if "truth" not in r:
            continue
        r["offset"] = round(r["truth"] - r["raw_s"], 3)
        a, b = bisect_left(gt, r["raw_s"] - 60), bisect_left(gt, r["raw_s"] + 60)
        neigh = [go[k] for k in range(a, b) if gt[k] != r["raw_s"]]
        if len(neigh) >= 3:
            med = float(np.median(neigh))
            r["local"] = round(med, 3)
            r["dev"] = round(r["offset"] - med, 3)
            strong = r["nmatch"] >= 4 and r["nmatch"] / r["ntok"] >= 0.7 and r["first_pos"] == 0
            if r["conf"] != "low" and abs(r["dev"]) > 1.5 and (not strong or abs(r["dev"]) > 3.0):
                r["pre_dev_conf"] = r["conf"]
                r["conf"] = "low"
                r["why"] = "offset far from its neighbours"
            if r["ntok"] == 1 and r["conf"] == "high" and abs(r["dev"]) > 0.7:
                r["pre_dev_conf"] = r["conf"]
                r["conf"] = "low"
                r["why"] = "one word, offset off its neighbours"
    for r in res:
        r.pop("j_last", None)
    return res


def cmd_lines(args):
    aligned, vtt, out = args[0], args[1], args[2]
    fit = None
    band = 15.0
    ref = None
    k = 3
    while k < len(args):
        if args[k] == "--fit":
            fit = load_fit(args[k + 1])
        elif args[k] == "--band":
            band = float(args[k + 1])
        elif args[k] == "--ref":
            ref = json.load(open(args[k + 1]))["lines"]
        k += 2
    words = load_words(aligned)
    cues = parse_vtt(vtt)
    for c in cues:
        c["toks"] = tokens(c["text"])
    sim = similarity_table({t for c in cues for t in c["toks"]}, {w["tok"] for w in words})
    if fit:
        expect = lambda t: map_fit(fit, t)  # noqa: E731
        prior = "fit"
    else:
        est = anchor_offsets(cues, words)
        ct = [c["s"] for c in cues]

        def expect(t):
            k = min(len(ct) - 1, max(0, bisect_left(ct, t)))
            return t + est[k]
        prior = "anchors"
        band = max(band, 20.0)
    res = match_lines(words, cues, sim, expect, band)
    nref = attach_ref(res, ref) if ref else None
    summary = summarize(res)
    if ref:
        summary["with_ref"] = nref
    json.dump({"subtitle": vtt, "words": aligned, "prior": prior, "band": band, "summary": summary, "lines": res},
              open(out, "w"), ensure_ascii=False, indent=0)
    print(json.dumps(summary))


def attach_ref(res, ref):
    """ref_s: the start of a reference track's line (the file's own track) that begins at the same
    spoken word as this line (truths within 0.05 s)."""
    rt = sorted((r["truth"], r["raw_s"]) for r in ref if r.get("conf") in ("high", "medium", "low"))
    rtimes = [x[0] for x in rt]
    n = 0
    for r in res:
        if "truth" in r:
            k = bisect_left(rtimes, r["truth"] - 0.05)
            if k < len(rt) and abs(rt[k][0] - r["truth"]) <= 0.05:
                r["ref_s"] = rt[k][1]
                d = r["truth"] - r["ref_s"]
                if abs(d) > 1.0 and r.get("conf") in ("high", "medium"):
                    # the own track starts the line (same word) elsewhere: the word's time is wrong
                    r["conf"], r["why"] = "low", "the file's own track puts this word elsewhere"
                elif abs(d) <= 0.5 and "pre_dev_conf" in r and r.get("conf") == "low" and \
                        r.get("nmatch", 0) >= 4 and r["nmatch"] / r["ntok"] >= 0.7:
                    # an unambiguous line whose word time the own track confirms: the subtitle
                    # mistimed it
                    r["conf"] = r.pop("pre_dev_conf")
                    r["why"] = "offset off its neighbours, confirmed by the file's own track"
                n += r.get("conf") in ("high", "medium")
    return n


def summarize(res):
    from collections import Counter
    c = Counter(r.get("conf") for r in res)
    offs = np.array([r["offset"] for r in res if r.get("conf") == "high"])
    s = {"cues": len(res), "conf": dict(c)}
    if len(offs):
        s["offset_median"] = round(float(np.median(offs)), 3)
    return s


# ---------------------------------------------------------------- PGS + OCR

def cmd_pgsvtt(args):
    import os
    cues = json.load(open(args[0]))["cues"]
    imgdir, out = args[1], args[2]

    def fmt(t):
        h, r = divmod(t, 3600)
        m, s = divmod(r, 60)
        return f"{int(h):02d}:{int(m):02d}:{s:06.3f}"
    with open(out, "w") as f:
        f.write("WEBVTT\n\n")
        for c in cues:
            p = os.path.join(imgdir, c.get("img", "").replace(".pgm", ".txt"))
            text = open(p).read().strip() if c.get("img") and os.path.exists(p) else ""
            text = re.sub(r"\n\s*\n", "\n", text)
            if text:
                f.write(f"{fmt(c['s'])} --> {fmt(c['e'])}\n{text}\n\n")


# ---------------------------------------------------------------- same timing?

def cmd_sametiming(args):
    a, b = parse_vtt(args[0]), parse_vtt(args[1])
    bs = np.array([c["s"] for c in b])
    be = np.array([c["e"] for c in b])
    best = None
    for scale in (1.0, 1.001001, 1 / 1.001001, 25 / 23.976, 23.976 / 25, 24 / 25, 25 / 24):
        for off in np.arange(-30, 30.001, 0.05):
            p = np.array([c["s"] for c in a]) * scale + off
            k = np.clip(np.searchsorted(bs, p), 1, len(bs) - 1)
            d = np.minimum(np.abs(bs[k] - p), np.abs(bs[k - 1] - p))
            n = int(np.sum(d <= 0.1))
            if best is None or n > best[0]:
                best = (n, scale, off)
    n, scale, off = best
    p = np.array([c["s"] for c in a]) * scale + off
    pe = np.array([c["e"] for c in a]) * scale + off
    k = np.clip(np.searchsorted(bs, p), 1, len(bs) - 1)
    near = np.where(np.abs(bs[k] - p) < np.abs(bs[k - 1] - p), k, k - 1)
    ds = np.abs(bs[near] - p)
    de = np.abs(be[near] - pe)
    shared = float(np.mean(ds <= 0.1)) >= 0.8
    print(json.dumps({"a_cues": len(a), "b_cues": len(b), "best_scale": scale, "best_offset": round(off, 2),
                      "starts_within_0.1": round(float(np.mean(ds <= 0.1)), 3),
                      "starts_within_0.5": round(float(np.mean(ds <= 0.5)), 3),
                      "both_ends_within_0.1": round(float(np.mean((ds <= 0.1) & (de <= 0.1))), 3),
                      "shared": shared}))
    if "--check" in args:
        sys.exit(0 if shared else 3)


def cmd_transfer(args):
    """Shared raw timing: every cue of <other.vtt> takes the truth of the EN line starting with it."""
    en = json.load(open(args[0]))
    other, out = parse_vtt(args[1]), args[2]
    lines = [r for r in en["lines"] if "truth" in r]
    es = np.array([r["raw_s"] for r in lines])
    best = None
    for scale in (1.0, 1.001001, 1 / 1.001001, 25 / 23.976, 23.976 / 25, 24 / 25, 25 / 24):
        for off in np.arange(-30, 30.001, 0.05):
            p = np.array([c["s"] for c in other]) * scale + off
            k = np.clip(np.searchsorted(es, p), 1, len(es) - 1)
            n = int(np.sum(np.minimum(np.abs(es[k] - p), np.abs(es[k - 1] - p)) <= 0.1))
            if best is None or n > best[0]:
                best = (n, scale, off)
    _, scale, off = best
    res = []
    for c in other:
        p = c["s"] * scale + off
        k = int(np.clip(np.searchsorted(es, p), 1, len(es) - 1))
        q = k if abs(es[k] - p) < abs(es[k - 1] - p) else k - 1
        r = {"i": c["i"], "raw_s": c["s"], "raw_e": c["e"], "text": c["text"]}
        if abs(es[q] - p) <= 0.1:
            src = lines[q]
            r.update(truth=src["truth"], conf=src["conf"], offset=round(src["truth"] - c["s"], 3), from_line=src["i"])
        else:
            r.update(conf="none", why="no line of the timed subtitle starts with it")
        res.append(r)
    from collections import Counter
    summary = {"cues": len(res), "scale": scale, "offset": round(off, 2), "conf": dict(Counter(r["conf"] for r in res))}
    json.dump({"subtitle": args[1], "method": "transfer", "summary": summary, "lines": res}, open(out, "w"),
              ensure_ascii=False, indent=0)
    print(json.dumps(summary))


def cmd_words(args):
    """Compact word list (the truth deliverable words.json)."""
    d = json.load(open(args[0]))
    out = [{"w": w["w"], "s": w["s"], "e": w["e"], "ws": w["ws"], "cs": w.get("cs"), "on": w.get("on")}
           for w in d["words"]]
    json.dump({"source": args[0], "model": d.get("model"), "aligner": d.get("aligner"),
               "note": "s/e: CTC-aligned start/end (file time, s); ws: whisper's start; cs: CTC score; "
                       "on: energy onset after a pause", "words": out}, open(args[1], "w"), ensure_ascii=False)


# ---------------------------------------------------------------- structural truth

def viterbi_offsets(raw, onsets, scale, grid, width=0.3, jump=3.0, per_s=0.3, gaps=None):
    """Piecewise constant offsets per cue: emission = closeness of the cue start to an onset."""
    K = len(grid)
    step = grid[1] - grid[0]
    n = len(raw)
    em = np.zeros((n, K))
    for i, r in enumerate(raw):
        p = r * scale + grid
        k = np.clip(np.searchsorted(onsets, p), 1, len(onsets) - 1)
        d = np.minimum(np.abs(onsets[k] - p), np.abs(onsets[k - 1] - p))
        em[i] = np.maximum(0.0, 1.0 - d / width)
    V = em[0].copy()
    back = np.zeros((n, K), dtype=np.int32)
    idx = np.arange(K)
    a = per_s * step
    for i in range(1, n):
        b = jump * (0.6 if gaps is not None and gaps[i] >= 4.0 else 1.0)
        # L1 distance transform: D[k] = max_k' V[k'] - a|k-k'| with argmax
        D, Darg = V.copy(), idx.copy()
        for k in range(1, K):
            if D[k - 1] - a > D[k]:
                D[k], Darg[k] = D[k - 1] - a, Darg[k - 1]
        for k in range(K - 2, -1, -1):
            if D[k + 1] - a > D[k]:
                D[k], Darg[k] = D[k + 1] - a, Darg[k + 1]
        jumpv = D - b
        stay = V >= jumpv
        back[i] = np.where(stay, idx, Darg)
        V = np.where(stay, V, jumpv) + em[i]
    path = np.zeros(n, dtype=np.int64)
    path[-1] = int(np.argmax(V))
    for i in range(n - 1, 0, -1):
        path[i - 1] = back[i, path[i]]
    return grid[path], float(V.max()), em


def cmd_struct(args):
    aligned, vtt, out = args[0], args[1], args[2]
    fit, en_lines, ref = None, None, None
    k = 3
    while k < len(args):
        if args[k] == "--fit":
            fit = load_fit(args[k + 1])
        elif args[k] == "--lines":
            en_lines = json.load(open(args[k + 1]))["lines"]
        elif args[k] == "--ref":
            ref = json.load(open(args[k + 1]))["lines"]
        k += 2
    words = load_words(aligned)
    onsets = phrase_onsets(words)
    allstarts = np.array(sorted({w["s"] for w in words if w["first"]}))
    cues = [c for c in parse_vtt(vtt) if c["text"].strip()]
    raw = np.array([c["s"] for c in cues])
    gaps = np.concatenate([[99.0], raw[1:] - np.array([c["e"] for c in cues[:-1]])])
    best = None
    tried = {}
    for scale in (1.0, 1.001001, 1 / 1.001001, 25 / 23.976, 23.976 / 25, 24 / 25, 25 / 24):
        if fit:
            # the fit's offsets, widened by what another frame rate moves over the file
            drift = abs(scale - fit["scale"]) * raw[-1]
            lo = min(s["offset"] for s in fit["segments"]) - 6 - drift
            hi = max(s["offset"] for s in fit["segments"]) + 6 + drift
            if drift > 30:
                continue
        else:
            lo, hi = -40.0, 40.0
        grid = np.round(np.arange(lo, hi + 1e-9, 0.02), 3)
        offs, score, em = viterbi_offsets(raw, onsets, scale, grid, gaps=gaps)
        tried[round(scale, 6)] = round(score, 1)
        if best is None or score > best[1]:
            best = (scale, score, offs)
    scale, score, offs = best
    en_starts = np.array(sorted(r["truth"] for r in (en_lines or []) if r.get("conf") in ("high", "medium")))
    res = []
    for i, c in enumerate(cues):
        p = c["s"] * scale + offs[i]
        k = np.clip(np.searchsorted(onsets, p), 1, len(onsets) - 1)
        o = onsets[k] if abs(onsets[k] - p) < abs(onsets[k - 1] - p) else onsets[k - 1]
        # run support: share of the 10 cues around that sit within 0.25 s of an onset at their offset
        a, b = max(0, i - 5), min(len(cues), i + 6)
        sup = []
        for q in range(a, b):
            pq = cues[q]["s"] * scale + offs[q]
            kq = np.clip(np.searchsorted(onsets, pq), 1, len(onsets) - 1)
            sup.append(min(abs(onsets[kq] - pq), abs(onsets[kq - 1] - pq)) <= 0.25)
        support = float(np.mean(sup))
        r = {"i": c["i"], "raw_s": c["s"], "raw_e": c["e"], "text": c["text"], "offset_run": round(float(offs[i]), 3),
             "pred": round(float(p), 3), "support": round(support, 2)}
        d = float(o - p)
        if abs(d) <= 0.4:
            r["truth"], r["snap"] = round(float(o), 3), round(d, 3)
            r["conf"] = "high" if abs(d) <= 0.2 and support >= 0.6 else "medium"
        else:
            kk = np.clip(np.searchsorted(allstarts, p), 1, len(allstarts) - 1)
            w = allstarts[kk] if abs(allstarts[kk] - p) < abs(allstarts[kk - 1] - p) else allstarts[kk - 1]
            if abs(w - p) <= 0.25:
                r["truth"], r["snap"], r["conf"] = round(float(w), 3), round(float(w - p), 3), "low"
                r["why"] = "nearest word start (no phrase onset within 0.4 s)"
            else:
                r["conf"] = "none"
                r["why"] = "no speech start near the predicted time"
        if "truth" in r:
            r["offset"] = round(r["truth"] - c["s"], 3)
            if len(en_starts):
                ke = np.clip(np.searchsorted(en_starts, r["truth"]), 1, len(en_starts) - 1)
                r["en_line_at"] = bool(min(abs(en_starts[ke] - r["truth"]), abs(en_starts[ke - 1] - r["truth"])) <= 0.05)
        res.append(r)
    from collections import Counter
    nref = attach_ref(res, ref) if ref else None
    summary = {"cues": len(res), "scale": scale, "scales_tried": tried, "conf": dict(Counter(r["conf"] for r in res)),
               "runs": runs_of(cues, offs, scale)}
    if len(en_starts):
        summary["truth_at_en_line_start"] = round(float(np.mean([r.get("en_line_at", False) for r in res if "truth" in r])), 3)
    if ref:
        summary["with_ref"] = nref
    json.dump({"subtitle": vtt, "words": aligned, "method": "struct", "summary": summary, "lines": res},
              open(out, "w"), ensure_ascii=False, indent=0)
    print(json.dumps(summary))


def runs_of(cues, offs, scale):
    out = []
    for i, c in enumerate(cues):
        if not out or abs(offs[i] - out[-1]["_o"]) > 1e-6:
            out.append({"from_raw": c["s"], "from_file": round(c["s"] * scale + float(offs[i]), 2),
                        "offset": round(float(offs[i]), 2), "cues": 0, "_o": float(offs[i])})
        out[-1]["cues"] += 1
    for r in out:
        r.pop("_o")
    return out


# ---------------------------------------------------------------- validation and scores

def stats(err):
    e = np.asarray(err, dtype=float)
    if len(e) == 0:
        return {"n": 0}
    return {"n": int(len(e)), "median": round(float(np.median(e)), 3), "mean": round(float(np.mean(e)), 3),
            "p10": round(float(np.percentile(e, 10)), 3), "p90": round(float(np.percentile(e, 90)), 3),
            "mad": round(float(np.median(np.abs(e - np.median(e)))), 3),
            "abs_le_0.1": round(float(np.mean(np.abs(e) <= 0.1)), 3),
            "abs_le_0.2": round(float(np.mean(np.abs(e) <= 0.2)), 3),
            "abs_le_0.3": round(float(np.mean(np.abs(e) <= 0.3)), 3),
            "abs_le_0.5": round(float(np.mean(np.abs(e) <= 0.5)), 3)}


def cmd_validate(args):
    d = json.load(open(args[0]))
    levels = ("high",) if "--min" not in args or args[args.index("--min") + 1] == "high" else ("high", "medium")
    ls = [r for r in d["lines"] if r.get("conf") in levels]
    # the own track is made for the file: its raw start is the reference; ASR start - track start
    err = [r["truth"] - r["raw_s"] for r in ls]
    err_ws = [r["truth_ws"] - r["raw_s"] for r in ls if r.get("truth_ws") is not None]
    err_on = [r["truth_on"] - r["raw_s"] for r in ls if r.get("truth_on") is not None]
    st = stats(err)
    # a track timed to the file: lines start at the speech (median within 0.3 s, 60 % within 0.3 s)
    timed = st.get("n", 0) >= 50 and abs(st["median"]) <= 0.3 and st["abs_le_0.3"] >= 0.6
    print(json.dumps({"ctc": st, "whisper": stats(err_ws), "energy_onset": stats(err_on),
                      "timed_to_file": timed}, indent=1))
    if "--check" in args:
        sys.exit(0 if timed else 3)


def pair_served(lines, served):
    """Served cue for every raw line: same order, same text (cues dropped at the start allowed)."""
    def key(t):
        return re.sub(r"\W+", "", t.lower())
    out = {}
    keys = [key(c["text"]) for c in served]
    used = set()
    # shift: served index - raw index (the server drops cues that end before 0, and sorts the served
    # cues by start, so refined neighbours can swap places)
    shift = 0
    for r in sorted(lines, key=lambda x: x["i"]):
        k = key(r["text"])
        for d in sorted(range(shift - 40, shift + 41), key=lambda x: (abs(x - shift), abs(x))):
            q = r["i"] + d
            if 0 <= q < len(served) and q not in used and keys[q] == k:
                out[r["i"]] = served[q]
                used.add(q)
                if abs(d - shift) > 2 or d == 0:
                    shift = d  # a swap of neighbours does not move the shift
                break
    return out


def cmd_score(args):
    d = json.load(open(args[0]))
    served = parse_vtt(args[1])
    rng = None
    label = args[1]
    levels = ("high", "medium")
    use = "truth"
    k = 2
    while k < len(args):
        if args[k] == "--range":
            rng = (float(args[k + 1]), float(args[k + 2]))
            k += 3
        elif args[k] == "--label":
            label = args[k + 1]
            k += 2
        elif args[k] == "--conf":
            levels = tuple(args[k + 1].split(","))
            k += 2
        elif args[k] == "--use":
            use = args[k + 1]
            k += 2
        else:
            k += 1
    pairs = pair_served(d["lines"], served)
    rows = []
    for r in d["lines"]:
        if r.get("conf") in levels and r["i"] in pairs and use in r:
            if rng and not (rng[0] <= r["truth"] < rng[1]):
                continue
            rows.append((r["truth"], pairs[r["i"]]["s"] - r[use]))
    paired = len(pairs)
    err = [e for _, e in rows]
    out = {"label": label, "use": use, "paired": paired, "lines": len(rows), "all": stats(err)}
    per_min = {}
    for t, e in rows:
        per_min.setdefault(int(t // 60), []).append(e)
    out["per_minute"] = {m: {"n": len(v), "median": round(float(np.median(v)), 2),
                             "le_0.5": round(float(np.mean(np.abs(np.array(v)) <= 0.5)), 2)}
                         for m, v in sorted(per_min.items())}
    print(json.dumps(out))


if __name__ == "__main__":
    {"lines": cmd_lines, "pgsvtt": cmd_pgsvtt, "sametiming": cmd_sametiming, "struct": cmd_struct,
     "transfer": cmd_transfer, "words": cmd_words, "validate": cmd_validate,
     "score": cmd_score}[sys.argv[1]](sys.argv[2:])
