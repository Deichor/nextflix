#!/usr/bin/env bash
# Ground truth of subtitle timing for one played file, from its own audio (see README.md).
#
# Usage (in WSL):
#   truth.sh <name> <hash> <index> (--en <vtt> | --en-id <id>) [options]
#     <name>                    run name; output in $NFX_TRUTH_DIR/<name> (default ~/nfx-truth/<name>)
#     --en <vtt> / --en-id <id> the English subtitle: a raw file, or its id as the server logs it
#                               ("subf2m/en/the-mentalist-third-season|english|3576031|3|2|1"): the raw
#                               file, the served (timed) cues and the fit are then copied from the server
#     --other <vtt> / --other-id <id>   a subtitle in another language, the same way
#     --other-lang <code>       its name in the outputs (default other)
#     --en-fit / --other-fit <json>     the server's fit (synced/<key>.json), if not taken from an id
#     --served-en / --served-other <vtt> the served cues (synced/<key>.vtt), if not taken from an id
#     --mt                      the other language also by machine translation (mt.py, mtmatch.py)
#     --watched <A> <B>         score this stretch of the file (seconds) on its own
#     --watch-spec <en|other> <json>   score the fits the viewer got (fits_watch.py; see README)
#     --reuse "<query>"         resolve query for the reuse endpoint (registers a TorBox source)
#     --model <name>            whisper model (default medium.en); --readrate <x> (default 40)
#     --dry-run                 print the plan and stop
#
# Steps whose output exists are skipped (delete a file to redo its step). Media are read once, through
# the server container (media.sh); everything else runs in throwaway containers (pyrun.sh); an ASR step
# waits until 6 GB of memory are available.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/common.sh"
ROOT=$(cd "$here/../.." && pwd)
usage() { sed -n '2,25p' "$0"; exit 1; }
[ $# -ge 3 ] || usage
name=$1; hash=$(echo "$2" | tr 'A-F' 'a-f'); index=$3; shift 3
[[ $name =~ ^[A-Za-z0-9._-]+$ ]] || { echo "name: letters, digits, . _ - only" >&2; exit 1; }
[[ $hash =~ ^[0-9a-f]{40}$ ]] || { echo "hash: 40 hex digits" >&2; exit 1; }
[[ $index =~ ^[0-9]+$ ]] || { echo "index: a number" >&2; exit 1; }
EN= ENID= ENFIT= SEN= OTHER= OTHERID= OLANG=other OTHERFIT= SOTHER= MT=0 WATCHED= SPEC= SPECSUB= MODEL=medium.en RATE=40 DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --en) EN=$2; shift 2;;
    --en-id) ENID=$2; shift 2;;
    --en-fit) ENFIT=$2; shift 2;;
    --served-en) SEN=$2; shift 2;;
    --other) OTHER=$2; shift 2;;
    --other-id) OTHERID=$2; shift 2;;
    --other-lang) OLANG=$2; shift 2;;
    --other-fit) OTHERFIT=$2; shift 2;;
    --served-other) SOTHER=$2; shift 2;;
    --mt) MT=1; shift;;
    --watched) WATCHED="$2 $3"; shift 3;;
    --watch-spec) SPECSUB=$2; SPEC=$3; shift 3;;
    --reuse) export REUSE_QUERY=$2; shift 2;;
    --model) MODEL=$2; shift 2;;
    --readrate) RATE=$2; shift 2;;
    --dry-run) DRY=1; shift;;
    *) echo "unknown option $1" >&2; usage;;
  esac
done
[ -n "$EN$ENID" ] || { echo "--en or --en-id is required" >&2; exit 1; }
[[ $OLANG =~ ^[a-z]{2,8}$ ]] || { echo "--other-lang: a short language code" >&2; exit 1; }
[ -z "$SPEC" ] || [ "$SPECSUB" = en ] || [ "$SPECSUB" = other ] || { echo "--watch-spec en|other <json>" >&2; exit 1; }
out="${NFX_TRUTH_DIR:-$HOME/nfx-truth}/$name"
case "$(realpath -m "$out")/" in "$ROOT"/*) echo "the output must stay outside the repo ($out)" >&2; exit 1;; esac

# A subtitle id as logged ("<source>/<lang>/<ref>"): the raw file is <source>/sha1("<source>|<ref>"),
# its timed result for this file synced/sha1("<source>|<ref>|<hash>|<index>") (.json fit, .vtt cues)
key_of() { docker exec "$TRUTH_SERVER" sh -c 'printf "%s\0" "$1" | sha1sum | cut -c1-40' _ "$1"; }
fetch_id() { # <id> <prefix>: copies <prefix>.vtt, <prefix>_fit.json, served_<prefix>.vtt into in/
  local id=$1 p=$2 source ref k
  source=${id%%/*}; ref=${id#*/}; ref=${ref#*/}
  [ -n "$source" ] && [ "$ref" != "$id" ] || { echo "bad subtitle id: $id" >&2; exit 1; }
  k=$(key_of "$source|$ref")
  docker exec "$TRUTH_SERVER" cat "/app/data/subtitles/$source/$k.vtt" > "$out/in/$p.vtt" ||
    { echo "the raw subtitle of $id is not on the server ($source/$k.vtt)" >&2; exit 1; }
  k=$(key_of "$source|$ref|$hash|$index")
  docker exec "$TRUTH_SERVER" cat "/app/data/subtitles/synced/$k.json" > "$out/in/${p}_fit.json" 2>/dev/null ||
    { rm -f "$out/in/${p}_fit.json"; echo "no timed result of $id for this file (synced/$k)" >&2; }
  docker exec "$TRUTH_SERVER" cat "/app/data/subtitles/synced/$k.vtt" > "$out/in/served_$p.vtt" 2>/dev/null ||
    rm -f "$out/in/served_$p.vtt"
}

if [ "$DRY" = 1 ]; then
  echo "run: $name, file $hash:$index -> $out"
  echo "english: ${EN:-id $ENID}${ENFIT:+, fit $ENFIT}${SEN:+, served $SEN}"
  [ -z "$OTHER$OTHERID" ] || echo "$OLANG: ${OTHER:-id $OTHERID}${OTHERFIT:+, fit $OTHERFIT}${SOTHER:+, served $SOTHER}$( [ "$MT" = 1 ] && echo ", also by translation")"
  for f in media/audio.wav transcript.json aligned.json; do
    [ -f "$out/$f" ] && echo "have $f (step skipped)" || echo "will make $f"
  done
  echo "model $MODEL, readrate $RATE${WATCHED:+, watched $WATCHED}${SPEC:+, watch spec ($SPECSUB) $SPEC}${REUSE_QUERY:+, reuse query set}"
  exit 0
fi

mkdir -p "$out/in"
[ -z "$ENID" ] || fetch_id "$ENID" en
[ -z "$EN" ] || cp "$EN" "$out/in/en.vtt"
[ -z "$ENFIT" ] || cp "$ENFIT" "$out/in/en_fit.json"
[ -z "$SEN" ] || cp "$SEN" "$out/in/served_en.vtt"
[ -z "$OTHERID" ] || fetch_id "$OTHERID" other
[ -z "$OTHER" ] || cp "$OTHER" "$out/in/other.vtt"
[ -z "$OTHERFIT" ] || cp "$OTHERFIT" "$out/in/other_fit.json"
[ -z "$SOTHER" ] || cp "$SOTHER" "$out/in/served_other.vtt"
[ -z "$SPEC" ] || cp "$SPEC" "$out/in/watch_spec.json"
echo '{"fit":{"scale":1,"segments":[{"from":0,"offset":0}]}}' > "$out/in/zero_fit.json"
export WORK=$out ASR_MEM=${ASR_MEM:-5g}
py() { bash "$here/pyrun.sh" "$@"; }
step() { echo "== $*" >&2; }
has() { [ -s "$out/in/$1" ]; }
wait_mem() { # other jobs share the WSL memory
  while [ "$(awk '/MemAvailable/{print int($2/1048576)}' /proc/meminfo)" -lt "${ASR_MIN_FREE_GB:-6}" ]; do
    echo "waiting for memory ($(awk '/MemAvailable/{print int($2/1048576)}' /proc/meminfo) GB available)" >&2
    sleep 30
  done
}

# 1. One read of the file: dialogue audio and the file's own subtitle tracks
if [ ! -f "$out/media/audio.wav" ]; then
  step "reading the file"
  bash "$here/media.sh" extract "$hash" "$index" "$out/media" "$RATE"
fi

# 2. ASR (about 7 minutes for 43 minutes of audio on 16 cores) and CTC alignment (2 minutes)
if [ ! -f "$out/transcript.json" ]; then
  step "transcribing"; wait_mem
  py asr.py transcribe /work/media/audio.wav /work/transcript.json --model "$MODEL" --vad 0
fi
if [ ! -f "$out/aligned.json" ]; then
  step "aligning"; wait_mem
  py asr.py align /work/media/audio.wav /work/transcript.json /work/aligned.json
fi
py match.py words /work/aligned.json /work/words.json

# 3. The file's own English tracks; the first one timed to the file becomes the reference (ref_s)
REF=
while read -r n codec lang sdh; do
  [ "$lang" = eng ] || continue
  if [ "$codec" = hdmv_pgs_subtitle ]; then
    [ -f "$out/media/sub_$n.sup" ] || continue
    if [ ! -f "$out/own_$n.vtt" ]; then
      step "OCR of PGS track $n"
      py pgs.py "/work/media/sub_$n.sup" "/work/own_${n}_cues.json" "/work/own_${n}_img"
      bash "$here/ocr.sh" "$out/own_${n}_img" >/dev/null
      py match.py pgsvtt "/work/own_${n}_cues.json" "/work/own_${n}_img" "/work/own_$n.vtt"
    fi
  else
    [ -f "$out/media/sub_$n.vtt" ] || continue
    cp "$out/media/sub_$n.vtt" "$out/own_$n.vtt"
  fi
  step "own track $n ($codec, sdh=$sdh)"
  py match.py lines /work/aligned.json "/work/own_$n.vtt" "/work/own_${n}_lines.json" --fit /work/in/zero_fit.json --band 20
  if py match.py validate "/work/own_${n}_lines.json" --check > "$out/own_${n}_validate.json"; then
    [ -n "$REF" ] || REF="/work/own_${n}_lines.json"
  fi
done < <(py probe_subs.py /work/media/probe.json)
[ -z "$REF" ] || step "reference: $REF"

# 4. English: text truth
step "English lines"
args=(lines /work/aligned.json /work/in/en.vtt /work/en_lines.json)
! has en_fit.json || args+=(--fit /work/in/en_fit.json)
[ -z "$REF" ] || args+=(--ref "$REF")
py match.py "${args[@]}"

# 5. The other language: shared raw timing -> transfer, else structural (and by translation with --mt)
if has other.vtt; then
  step "$OLANG lines"
  if py match.py sametiming /work/in/en.vtt /work/in/other.vtt --check; then
    py match.py transfer /work/en_lines.json /work/in/other.vtt "/work/${OLANG}_lines.json"
  else
    args=(struct /work/aligned.json /work/in/other.vtt "/work/${OLANG}_lines.json" --lines /work/en_lines.json)
    ! has other_fit.json || args+=(--fit /work/in/other_fit.json)
    [ -z "$REF" ] || args+=(--ref "$REF")
    py match.py "${args[@]}"
  fi
  if [ "$MT" = 1 ] && has other_fit.json; then
    [ -f "$out/${OLANG}_mt.json" ] || ASR_CPUS=4 py mt.py /work/in/other.vtt "/work/${OLANG}_mt.json" --lang "$OLANG"
    py mtmatch.py /work/aligned.json "/work/${OLANG}_mt.json" "/work/${OLANG}_lines_mt.json" --fit /work/in/other_fit.json
    py cmp_truths.py "/work/${OLANG}_lines_mt.json" "/work/${OLANG}_lines.json"
  fi
fi

# 6. Scores of the served timings
args=()
! has served_en.vtt || args+=(--sub EN /work/en_lines.json /work/in/served_en.vtt)
! has served_other.vtt || args+=(--sub "$(echo "$OLANG" | tr a-z A-Z)" "/work/${OLANG}_lines.json" /work/in/served_other.vtt)
if [ ${#args[@]} -gt 0 ]; then
  [ -z "$WATCHED" ] || args+=(--watched $WATCHED)
  py report.py "${args[@]}" > "$out/report.md"
  # served (line refinement included) against the fit alone
  for p in en other; do
    if has "served_$p.vtt" && has "${p}_fit.json"; then
      lines=/work/en_lines.json; [ "$p" = en ] || lines="/work/${OLANG}_lines.json"
      py refine_eval.py "$lines" "/work/in/$p.vtt" "/work/in/served_$p.vtt" "/work/in/${p}_fit.json" --label "$p: served vs fit only" >> "$out/report.md"
    fi
  done
  if has watch_spec.json; then
    lines=/work/en_lines.json raw=/work/in/en.vtt
    [ "$SPECSUB" = en ] || { lines="/work/${OLANG}_lines.json"; raw=/work/in/other.vtt; }
    { echo; echo "### Fits the viewer got"; echo; py fits_watch.py "$lines" "$raw" /work/in/watch_spec.json; } >> "$out/report.md"
  fi
  step "report: $out/report.md"
fi
step "done: $out"
