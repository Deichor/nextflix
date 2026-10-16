"""ASR word timings for subtitle-timing ground truth.

Runs in a throwaway container (pyrun.sh, venv and models in the volume set up by setup.sh). Two steps:

  asr.py transcribe <audio.wav> <out.json> [--model medium.en] [--start S --end E] [--vad 0|1]
      faster-whisper (CPU, int8) with word timestamps; no VAD by default (it dropped phone and radio
      lines). audio.wav is 16 kHz mono PCM on the file's
      timeline (media.sh extract).
  asr.py align <audio.wav> <transcribe.json> <out.json>
      Re-times every word by CTC forced alignment (torchaudio WAV2VEC2_ASR_LARGE_LV60K_960H, 20 ms
      frames) of whisper's own words, in chunks cut at pauses, and adds an energy onset (first 10 ms
      frame 8 dB above the quiet before it) for words that follow a pause.

Output words: {"w": text, "s": start, "e": end, "ws": whisper start, "we": whisper end,
"p": whisper probability, "cs": CTC score, "on": energy onset or null, "seg": segment index}.
"""
import json
import math
import os
import re
import sys
import time
import wave

import numpy as np

SR = 16000
# Model versions the measurements were made with (Hugging Face commits)
WHISPER_REVISIONS = {"medium.en": "a29b04bd15381511a9af671baec01072039215e3"}


def load_wav(path, start=None, end=None):
    with wave.open(path, "rb") as w:
        assert w.getframerate() == SR and w.getnchannels() == 1 and w.getsampwidth() == 2, "need 16 kHz mono s16"
        n = w.getnframes()
        a = 0 if start is None else int(start * SR)
        b = n if end is None else min(n, int(end * SR))
        w.setpos(a)
        raw = w.readframes(b - a)
    return np.frombuffer(raw, dtype=np.int16).astype(np.float32) / 32768.0, a / SR


def transcribe(args):
    from faster_whisper import WhisperModel

    wav, out = args[0], args[1]
    model_name, start, end, vad = "medium.en", None, None, False
    i = 2
    while i < len(args):
        if args[i] == "--model":
            model_name = args[i + 1]
        elif args[i] == "--start":
            start = float(args[i + 1])
        elif args[i] == "--end":
            end = float(args[i + 1])
        elif args[i] == "--vad":
            vad = args[i + 1] == "1"
        i += 2
    audio, t0 = load_wav(wav, start, end)
    threads = int(os.environ.get("ASR_THREADS", "16"))
    model = WhisperModel(model_name, device="cpu", compute_type="int8", cpu_threads=threads,
                         download_root="/cache/models/fw", revision=WHISPER_REVISIONS.get(model_name))
    began = time.time()
    segments, info = model.transcribe(
        audio, language="en", beam_size=5, word_timestamps=True, condition_on_previous_text=False,
        vad_filter=vad, vad_parameters=dict(min_silence_duration_ms=500, speech_pad_ms=400) if vad else None)
    segs = []
    last_log = 0.0
    for s in segments:
        words = [{"w": w.word.strip(), "s": round(w.start + t0, 3), "e": round(w.end + t0, 3),
                  "p": round(w.probability, 3)} for w in (s.words or [])]
        segs.append({"s": round(s.start + t0, 3), "e": round(s.end + t0, 3), "text": s.text.strip(),
                     "lp": round(s.avg_logprob, 3), "nsp": round(s.no_speech_prob, 3), "words": words})
        if s.end - last_log >= 120:
            last_log = s.end
            el = time.time() - began
            print(f"  {s.end + t0:7.1f}s transcribed in {el:6.1f}s (x{s.end / max(el, 1e-6):.2f} real time)", flush=True)
    res = {"model": model_name, "vad": vad, "audio": wav, "start": t0, "duration": len(audio) / SR,
           "took": round(time.time() - began, 1), "segments": segs}
    with open(out, "w") as f:
        json.dump(res, f, ensure_ascii=False)
    print(f"transcribed {len(audio) / SR:.0f}s in {res['took']}s: {len(segs)} segments, "
          f"{sum(len(s['words']) for s in segs)} words", flush=True)


# --- CTC forced alignment -------------------------------------------------------------------

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


def spoken_chars(word):
    """The word as the aligner's alphabet (A-Z, apostrophe); digits spelled out."""
    w = word.lower().replace("’", "'").replace("%", " percent").replace("&", " and ").replace("$", " dollars ")
    w = re.sub(r"\d+", lambda m: " " + (num_words(int(m.group())) if len(m.group()) < 7 else
                                         " ".join(ONES[int(c)] for c in m.group())) + " ", w)
    w = re.sub(r"[^a-z' ]", "", w)
    return w.upper().split()


def energy_db(audio):
    """10 ms frame energies in dB."""
    n = len(audio) // 160
    fr = audio[: n * 160].reshape(n, 160).astype(np.float64)
    return 10 * np.log10(np.mean(fr * fr, axis=1) + 1e-10)


def energy_onset(db, t, lookback=0.6, before=0.25, after=0.15, rise=8.0):
    """First 10 ms frame near t that is rise dB above the quiet before it (and stays up 3 frames)."""
    i = int(round(t / 0.01))
    a, b = max(0, i - int(lookback / 0.01)), max(0, i - int(before / 0.01))
    if b - a < 10:
        return None
    floor = float(np.percentile(db[a:b], 20))
    for k in range(max(0, i - int(before / 0.01)), min(len(db) - 3, i + int(after / 0.01))):
        if db[k] >= floor + rise and db[k + 1] >= floor + rise and db[k + 2] >= floor + rise - 3:
            return round(k * 0.01, 3)
    return None


def align(args):
    import torch
    import torchaudio
    import torchaudio.functional as F

    wav, tr_path, out = args[0], args[1], args[2]
    torch.set_num_threads(int(os.environ.get("ASR_THREADS", "16")))
    tr = json.load(open(tr_path))
    words = []
    for si, s in enumerate(tr["segments"]):
        for w in s["words"]:
            if w["w"]:
                words.append({"w": w["w"], "ws": w["s"], "we": w["e"], "p": w["p"], "seg": si})
    audio, _ = load_wav(wav)
    total = len(audio) / SR
    bundle = torchaudio.pipelines.WAV2VEC2_ASR_LARGE_LV60K_960H
    model = bundle.get_model().eval()
    labels = bundle.get_labels()
    idx = {c: i for i, c in enumerate(labels)}
    sep = idx["|"]

    # Chunks: runs of words split at whisper pauses >= 0.8 s, at most ~30 s each
    chunks, cur = [], []
    for k, w in enumerate(words):
        if cur and (w["ws"] - words[cur[-1]]["we"] >= 0.8 or w["we"] - words[cur[0]]["ws"] > 30):
            chunks.append(cur)
            cur = []
        cur.append(k)
    if cur:
        chunks.append(cur)

    began = time.time()
    for ci, ch in enumerate(chunks):
        prev_end = words[chunks[ci - 1][-1]]["we"] if ci > 0 else 0.0
        next_start = words[chunks[ci + 1][0]]["ws"] if ci + 1 < len(chunks) else total
        a = max(0.0, words[ch[0]]["ws"] - min(1.0, max(0.2, (words[ch[0]]["ws"] - prev_end) / 2)))
        b = min(total, words[ch[-1]]["we"] + min(1.0, max(0.2, (next_start - words[ch[-1]]["we"]) / 2)))
        seq, owner = [], []
        for k in ch:
            toks = spoken_chars(words[k]["w"])
            for tok in toks:
                if seq:
                    seq.append(sep)
                    owner.append(None)
                for c in tok:
                    if c in idx:
                        seq.append(idx[c])
                        owner.append(k)
        if not seq:
            continue
        x = torch.from_numpy(audio[int(a * SR): int(b * SR)]).unsqueeze(0)
        with torch.inference_mode():
            em, _ = model(x)
            em = torch.log_softmax(em, dim=-1)
        T = em.shape[1]
        ratio = (b - a) / T
        if T < len(seq) + 2:
            continue
        try:
            ali, scores = F.forced_align(em, torch.tensor([seq], dtype=torch.int32), blank=0)
        except Exception as e:  # noqa: BLE001
            print(f"  chunk {ci} at {a:.1f}s: {e}", flush=True)
            continue
        ali, scores = ali[0].tolist(), scores[0].exp().tolist()
        # Token spans: walk the frame path; each new non-blank label (or repeat after blank) is the next target
        spans = []  # (target index, first frame, last frame, score sum, frames)
        ti, prev = -1, 0
        for f, lab in enumerate(ali):
            if lab != 0:
                if lab != prev:
                    ti += 1
                    spans.append([ti, f, f, scores[f], 1])
                else:
                    spans[-1][2] = f
                    spans[-1][3] += scores[f]
                    spans[-1][4] += 1
            prev = lab
        if len(spans) != len(seq):
            # repeated letters separated by blanks: fall back to torchaudio's merge
            tspans = F.merge_tokens(torch.tensor(ali), torch.tensor(scores))
            spans = [[i, s.start, s.end - 1, s.score * (s.end - s.start), s.end - s.start] for i, s in enumerate(tspans)]
            if len(spans) != len(seq):
                print(f"  chunk {ci} at {a:.1f}s: {len(spans)} spans for {len(seq)} targets", flush=True)
                continue
        per = {}
        for (t_i, f0, f1, sc, nf) in spans:
            k = owner[t_i]
            if k is None:
                continue
            d = per.setdefault(k, [f0, f1, 0.0, 0])
            d[0], d[1] = min(d[0], f0), max(d[1], f1)
            d[2] += sc
            d[3] += nf
        for k, (f0, f1, sc, nf) in per.items():
            words[k]["s"] = round(a + f0 * ratio, 3)
            words[k]["e"] = round(a + (f1 + 1) * ratio, 3)
            words[k]["cs"] = round(sc / max(nf, 1), 3)
        if ci % 50 == 0:
            print(f"  chunk {ci}/{len(chunks)} at {a:.0f}s ({time.time() - began:.0f}s)", flush=True)

    # Words the aligner could not place keep whisper's times
    for w in words:
        if "s" not in w:
            w["s"], w["e"], w["cs"] = w["ws"], w["we"], None
    db = energy_db(audio)
    for k, w in enumerate(words):
        gap = w["s"] - words[k - 1]["e"] if k > 0 else 9.0
        w["on"] = energy_onset(db, w["s"]) if gap >= 0.2 else None
    res = {"source": tr_path, "model": tr.get("model"), "aligner": "WAV2VEC2_ASR_LARGE_LV60K_960H",
           "took": round(time.time() - began, 1), "words": words}
    with open(out, "w") as f:
        json.dump(res, f, ensure_ascii=False)
    moved = [w["s"] - w["ws"] for w in words if w.get("cs") is not None]
    if moved:
        m = np.array(moved)
        print(f"aligned {len(moved)} of {len(words)} words in {res['took']}s; CTC - whisper start: median "
              f"{np.median(m):+.3f}s, p10 {np.percentile(m, 10):+.3f}, p90 {np.percentile(m, 90):+.3f}", flush=True)


if __name__ == "__main__":
    {"transcribe": transcribe, "align": align}[sys.argv[1]](sys.argv[2:])
