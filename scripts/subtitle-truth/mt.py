"""Machine translation of a subtitle's cues to English (Helsinki-NLP opus-mt-tc-big, CPU), so lines in another
language can be matched to the English transcript by text.

  mt.py <subs.vtt> <out.json> [--lang tr]

Every dialogue part of a cue ("-A / -B") is translated on its own; out.json: [{i, s, e, text, en}].
Runs in the container of pyrun.sh (setup.sh installs it).
"""
import json
import os
import re
import sys
import time

sys.path.append("/scripts")  # own modules after the standard library
from match import parse_vtt  # noqa: E402

# Model versions the measurements were made with (Hugging Face commits)
MT_REVISIONS = {"Helsinki-NLP/opus-mt-tc-big-tr-en": "2261c8fc7b1af59caee87f8ff0ecf3fbccfe8391"}


def parts_of(text):
    t = re.sub(r"<[^>]*>|\{[^}]*\}", " ", text)
    ps = [re.sub(r"\s+", " ", p).strip() for p in re.split(r"(?m)^\s*-\s*", t)]
    return [p for p in ps if p]


def main():
    import torch
    from transformers import MarianMTModel, MarianTokenizer

    src, out = sys.argv[1], sys.argv[2]
    lang = sys.argv[sys.argv.index("--lang") + 1] if "--lang" in sys.argv else "tr"
    torch.set_num_threads(int(os.environ.get("ASR_THREADS", "4")))
    # the tc-big models ship safetensors (the older opus-mt ones only a pickle, which torch < 2.6 must not load)
    name = f"Helsinki-NLP/opus-mt-tc-big-{lang}-en"
    rev = MT_REVISIONS.get(name)
    tok = MarianTokenizer.from_pretrained(name, revision=rev)
    model = MarianMTModel.from_pretrained(name, revision=rev, use_safetensors=True).eval()
    cues = parse_vtt(src)
    jobs = [(k, p) for k, c in enumerate(cues) for p in parts_of(c["text"])]
    done = {}
    began = time.time()
    for b in range(0, len(jobs), 32):
        batch = jobs[b:b + 32]
        enc = tok([p for _, p in batch], return_tensors="pt", padding=True, truncation=True, max_length=256)
        with torch.inference_mode():
            gen = model.generate(**enc, num_beams=3, max_new_tokens=128)
        for (k, _), g in zip(batch, tok.batch_decode(gen, skip_special_tokens=True)):
            done.setdefault(k, []).append(g)
    res = [{"i": c["i"], "s": c["s"], "e": c["e"], "text": c["text"], "en": " ".join(done.get(k, []))}
           for k, c in enumerate(cues)]
    json.dump(res, open(out, "w"), ensure_ascii=False, indent=0)
    print(f"translated {len(jobs)} parts of {len(cues)} cues in {time.time() - began:.0f}s")


if __name__ == "__main__":
    main()
