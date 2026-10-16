"""PGS (Blu-ray image subtitle, .sup) cue times and bitmaps.

  pgs.py <in.sup> <out.json> [<png_dir>]

Cue start = PTS of a presentation composition with objects, end = the next composition's PTS.
With png_dir, every cue's bitmap is written as an 8-bit grayscale PGM (dark text on white) for OCR.
Times are seconds on the timeline ffmpeg wrote the .sup with (media.sh: the file's timeline).
"""
import json
import os
import struct
import sys

import numpy as np


def segments(buf):
    i = 0
    while i + 13 <= len(buf):
        if buf[i:i + 2] != b"PG":
            raise ValueError(f"bad magic at {i}")
        pts, _dts, typ, size = struct.unpack(">IIBH", buf[i + 2:i + 13])
        yield pts / 90000.0, typ, buf[i + 13:i + 13 + size]
        i += 13 + size


def rle_decode(data, w, h):
    img = np.zeros((h, w), dtype=np.uint8)
    x = y = 0
    i = 0
    n = len(data)
    while i < n and y < h:
        b = data[i]
        i += 1
        if b:
            if x < w:
                img[y, x] = b
            x += 1
            continue
        b2 = data[i]
        i += 1
        if b2 == 0:
            x, y = 0, y + 1
            continue
        flag = b2 >> 6
        if flag == 0:
            run, col = b2 & 0x3F, 0
        elif flag == 1:
            run, col = ((b2 & 0x3F) << 8) | data[i], 0
            i += 1
        elif flag == 2:
            run, col = b2 & 0x3F, data[i]
            i += 1
        else:
            run, col = ((b2 & 0x3F) << 8) | data[i], data[i + 1]
            i += 2
        if col and x < w:
            img[y, x:min(w, x + run)] = col
        x += run
    return img


def main():
    sup, out = sys.argv[1], sys.argv[2]
    png_dir = sys.argv[3] if len(sys.argv) > 3 else None
    if png_dir:
        os.makedirs(png_dir, exist_ok=True)
    buf = open(sup, "rb").read()
    palettes = {}
    objects = {}  # id -> (w, h, data)
    pending = {}  # id -> bytearray while an object spans several ODS
    comps = []  # (pts, state, palette_id, [(obj_id, x, y)])
    cur = None
    for pts, typ, d in segments(buf):
        if typ == 0x16:  # PCS
            w, h, _fr, _num, state, _pupd, pal = struct.unpack(">HHBHBBB", d[:10])
            nobj = d[10]
            objs, k = [], 11
            for _ in range(nobj):
                oid, _win, crop, x, y = struct.unpack(">HBBHH", d[k:k + 8])
                k += 8 + (8 if crop & 0x80 else 0)
                objs.append((oid, x, y))
            cur = {"pts": pts, "state": state, "pal": pal, "objs": objs, "size": (w, h), "pupd": _pupd}
        elif typ == 0x14:  # PDS
            pid = d[0]
            pal = palettes.setdefault(pid, np.zeros((256, 2), dtype=np.float32))
            for k in range(2, len(d) - 4, 5):
                e, yv, _cr, _cb, a = d[k:k + 5]
                pal[e] = (yv, a)
        elif typ == 0x15:  # ODS
            oid, _ver, seq = struct.unpack(">HBB", d[:4])
            if seq & 0x80:
                _ln = int.from_bytes(d[4:7], "big")
                ow, oh = struct.unpack(">HH", d[7:11])
                pending[oid] = [ow, oh, bytearray(d[11:])]
            elif oid in pending:
                pending[oid][2] += d[4:]
            if seq & 0x40 and oid in pending:
                ow, oh, data = pending.pop(oid)
                objects[oid] = (ow, oh, bytes(data))
        elif typ == 0x80 and cur is not None:  # END: the display set is complete
            cur["bitmaps"] = []
            pal = palettes.get(cur["pal"])
            for oid, x, y in cur["objs"]:
                if oid in objects and pal is not None:
                    ow, oh, data = objects[oid]
                    cur["bitmaps"].append((x, y, ow, oh, data, pal.copy()))
            comps.append(cur)
            cur = None
    cues = []
    for i, c in enumerate(comps):
        if not c["objs"]:
            continue
        if c["pupd"] and cues and i > 0 and comps[i - 1]["objs"] == c["objs"]:
            continue  # a palette-only update of the shown cue
        end = comps[i + 1]["pts"] if i + 1 < len(comps) else c["pts"] + 5
        cue = {"i": len(cues), "s": round(c["pts"], 3), "e": round(end, 3), "n": len(c["objs"])}
        if png_dir and c["bitmaps"]:
            x0 = min(b[0] for b in c["bitmaps"])
            y0 = min(b[1] for b in c["bitmaps"])
            x1 = max(b[0] + b[2] for b in c["bitmaps"])
            y1 = max(b[1] + b[3] for b in c["bitmaps"])
            canvas = np.full((y1 - y0 + 40, x1 - x0 + 40), 255, dtype=np.uint8)
            for (x, y, ow, oh, data, pal) in c["bitmaps"]:
                idx = rle_decode(data, ow, oh)
                lum = pal[idx, 0] * (pal[idx, 1] / 255.0)  # white fill bright, outline/background dark
                ink = np.clip(255 - lum * 1.2, 0, 255).astype(np.uint8)
                region = canvas[y - y0 + 20:y - y0 + 20 + oh, x - x0 + 20:x - x0 + 20 + ow]
                np.minimum(region, ink, out=region)
            name = f"cue{cue['i']:05d}.pgm"
            with open(os.path.join(png_dir, name), "wb") as f:
                f.write(b"P5 %d %d 255\n" % (canvas.shape[1], canvas.shape[0]))
                f.write(canvas.tobytes())
            cue["img"] = name
        cues.append(cue)
    with open(out, "w") as f:
        json.dump({"source": sup, "cues": cues}, f)
    print(f"{len(comps)} compositions, {len(cues)} cues; first {cues[0]['s'] if cues else None}, "
          f"last {cues[-1]['s'] if cues else None}")


if __name__ == "__main__":
    main()
