# Subtitle timing ground truth

Measures where each subtitle line is really spoken in a played file, from the file's own audio, and scores
the timings the server served against it. It does not use the server's timing code, so it can tell
whether a reported slip was real and where (title sequence, act breaks, frame rate, line refinement).
Everything runs in throwaway containers; the media are read only through the server container
(read-only, one read at a time, not while a player streams).

## What it measures

- Speech: the dialogue channel (front centre of 5.1, else a downmix) is transcribed with faster-whisper
  `medium.en` (word timestamps, no VAD: VAD dropped phone and radio lines) and every word re-timed by CTC
  forced alignment (wav2vec2 large, 20 ms frames).
- English subtitle: its words are aligned to the transcript in order; a line's truth is the start of its
  first spoken word (`en_lines.json`: raw start, truth, offset = truth - raw start, confidence).
- Another language: when it shares the English raw timing, the English truth is copied; otherwise its
  frame rate and per-act offsets are fitted onto the spoken phrase starts and each cue snapped to the
  nearest one (`<lang>_lines.json`). `--mt` adds a check by machine translation (noisier).
- The file's own English tracks (text, or PGS through OCR) are matched too; one timed to the file
  (`own_<n>_validate.json`: `timed_to_file`) cross-checks every line (`ref_s`).
- `report.md`: offset runs with their drift (a wrong frame rate drifts 1 s per 1000 s), scores of the
  served cues (whole file, `--watched` stretch, per minute, lines more than 1 s off), the best any
  piecewise constant timing could do, and the served cues against the fit alone (line refinement).

## One-time setup

```
wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/scripts/subtitle-truth/setup.sh
```

Creates the venv (pinned in `requirements.lock`, image pinned in `common.sh`) in the volume
`nfx-asr-cache`; the models (about 4 GB) download into it on first use, tesseract for the OCR on the
first PGS track. Model versions are pinned in `asr.py` and `mt.py`.

## An episode the user names

1. The file and what the TV did: `scripts/playback-log.sh 3 S03E02` shows the sessions with `src=<hash8>:
   <index>`, positions (`pos=`) and subtitle events (`subs ... lang= provider= sync= why=poll moved=<n>`:
   when the TV took new cues). The full hash: `docker exec nextflix-server-1 sqlite3 -readonly
   /app/data/nextflix.db "select json_extract(result,'$.hash'), json_extract(result,'$.file_name') from
   stream_sources where json_extract(result,'$.hash') like 'a22f37b4%'"`.
2. The served subtitles: `docker logs nextflix-server-1 2>&1 | grep 'subtitles: timing of' | grep a22f37b4`
   gives `timing of <source>/<lang>/<ref> on <hash8>:<index>: <state> (scale, offsets ...)` for every fit
   served, provisional ones included. Keep these lines: they are lost when the container is recreated.
   From the id `truth.sh` copies the raw subtitle (`/app/data/subtitles/<source>/<sha1(source|ref)>.vtt`)
   and the timed result (`/app/data/subtitles/synced/<sha1(source|ref|hash|index)>.json` and `.vtt`).
   A `replaced` subtitle served another one's cues: use that one's id.
3. Run (in WSL; about 12 minutes):
   ```
   bash scripts/subtitle-truth/truth.sh S03E02 <hash> 1 \
     --en-id 'subf2m/en/the-mentalist-third-season|english|3576031|3|2|1' \
     --other-id 'subf2m/tr/the-mentalist-third-season|turkish|3578445|3|2|1' --other-lang tr \
     --watched 1132 2596
   ```
   Output: `~/nfx-truth/S03E02/` (`NFX_TRUTH_DIR` moves it; never inside the repo), `report.md` first.
   `--dry-run` shows the plan. A file the TorBox proxy does not serve yet needs `--reuse
   "type=tv&title=The%20Mentalist&season=3&episode=2&tmdb_id=5920"` (the call the TV makes on resume).
4. What the TV showed: a viewing gets several fits (provisional, then final). Write one entry per logged
   fit with the stretch the TV played while it was served (from the `moved=` events' positions; the
   `@117s` change points are on the scaled clock as logged) and pass `--watch-spec other spec.json`:
   ```
   [{"label": "fit of 02:24:03", "scale": 1.001001,
     "segments": [[0, 1.04], [117, 2.83], [554, 2.43], [817, 4.11], [1233, 5.87], [1519, 5.47], [1929, 6.95]],
     "watched": [1843.6, 1990.7]}]
   ```

## Cost

- One read of the whole file through the TorBox proxy (its size: 0.8-2 GB for these episodes), capped at
  40x real time (about 65 s for 43 minutes); a run waits while a player streams.
- CPU: ASR about 7 minutes for 43 minutes of audio on 16 cores (21 minutes on a busy machine), alignment 2,
  OCR 1, translation 2-3 minutes. Containers get 5 GB; ASR steps wait for 6 GB available.
- About 170 MB per episode under `~/nfx-truth/`.

## Validation (2026-10-16)

- The Mentalist S03E02 (d3g WEBRip) has an English SDH PGS track timed to the file: the ASR line starts lie
  within 0.1 / 0.2 / 0.3 s of its cue starts for 60 / 87 / 94 % of 756 lines (median +0.04 s); whisper's own
  word starts alone 81 % within 0.3 s.
- An own track is not always a reference: S02E16 "Code Red" carries one from another cut (+1.4 to +9.1 s at
  the act breaks), S03E01's PGS track too, and S02E22 NF WEB-DL's English tracks are roll-up closed captions
  (p10 / p90 -0.89 / +0.82 s per line). Line refinement onto those made the moved lines worse (S02E22:
  within 0.3 s 78 -> 39 % of 109 English lines).
- The truth does not depend on the server's fits (the same lines without them). The translation truth is
  noisy: 72 % of lines within 0.1 s of the structural one on S03E02.

## Files

| File | Does |
|---|---|
| `truth.sh` | entry: inputs, read, ASR, matching, report |
| `media.sh` | proxy URL, source registration, the one read (`extract`) |
| `pyrun.sh`, `common.sh`, `setup.sh`, `requirements.lock` | containers, pinned image, volume, venv |
| `asr.py` | transcription and CTC alignment |
| `pgs.py`, `ocr.sh` | PGS cue times and bitmaps, OCR |
| `match.py` | truths (`lines`, `struct`, `transfer`), own-track check (`validate`), `score` |
| `mt.py`, `mtmatch.py` | translation and its truth |
| `report.py`, `refine_eval.py`, `fits_watch.py` | scores: served, served vs fit alone, the fits a viewer got |
| `cmp_truths.py`, `names_check.py`, `probe_subs.py` | two truths compared, names/numbers check, track list |
