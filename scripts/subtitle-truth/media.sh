#!/usr/bin/env bash
# Media access of the truth runs. Everything runs in the server container (it has ffmpeg, ffprobe,
# curl, sha1sum); the file is only read, nothing is written there except a temp folder that is removed.
#
#   media.sh url <hash> <index>
#       Prints the TorBox loopback proxy URL serving the file (http://127.0.0.1:<port>/f/<hash>/<index>),
#       registering the source when needed: the saved-source adoption (files?magnet=, for stream_sources
#       rows marked debrid), then, with REUSE_QUERY set (e.g. "type=tv&title=The%20Mentalist&season=3&
#       episode=2&tmdb_id=5920"), the reuse endpoint a player calls when it resumes (it holds the title
#       like a player). Exit 2 when the file is not served by the proxy.
#   media.sh extract <hash> <index> <outdir> [readrate]
#       One sequential read: probe.json, the dialogue channel (FC of a layout with a centre, else a
#       downmix) as 16 kHz mono WAV on the file's timeline (aresample first_pts=0 like the speech tap:
#       sample 0 = file time 0), every text subtitle track as WebVTT (sub_<stream>.vtt) and every PGS
#       track copied (sub_<stream>.sup). readrate (default 40) caps the read at that many times real
#       time. One read at a time (a lock), and only while nothing plays (the newest playback-log line
#       older than 2 minutes; waits up to TRUTH_WAIT_MAX seconds, default 1800; TRUTH_NO_WAIT=1 skips).
#       NO_AUDIO=1 skips the audio (subtitle tracks only).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/common.sh"
C=$TRUTH_SERVER

proxy_ports() {
  docker exec "$C" cat /proc/net/tcp | awk 'NR>1 && $4=="0A" && substr($2,1,8)=="0100007F"{print substr($2,10,4)}' |
    while read -r hex; do echo $((16#$hex)); done
}

head_code() { docker exec "$C" curl -s -o /dev/null -w '%{http_code}' -I --max-time 20 "$1" || true; }

find_url() {
  local hash=$1 index=$2 p code
  for p in $(proxy_ports); do
    code=$(head_code "http://127.0.0.1:$p/f/$hash/$index")
    if [ "$code" = 200 ] || [ "$code" = 206 ]; then echo "http://127.0.0.1:$p/f/$hash/$index"; return 0; fi
  done
  return 1
}

# Waits while a player streams (its playback log grows every minute while it plays)
wait_idle() {
  [ "${TRUTH_NO_WAIT:-0}" = 1 ] && return 0
  local waited=0 last now
  while :; do
    last=$(docker exec "$C" sh -c 'f=$(ls -t /app/data/playback-logs/*.log 2>/dev/null | head -1); [ -n "$f" ] && tail -n 1 "$f" | cut -c1-19' || true)
    now=$(date -u +%s)
    if [ -z "$last" ] || [ $((now - $(date -u -d "$last" +%s 2>/dev/null || echo 0))) -ge 120 ]; then return 0; fi
    if [ "$waited" -ge "${TRUTH_WAIT_MAX:-1800}" ]; then echo "a player is still streaming; not reading now" >&2; exit 3; fi
    echo "a player is streaming (last playback log line $last UTC); waiting" >&2
    sleep 30
    waited=$((waited + 30))
  done
}

cmd=${1:-}; shift || true
case "$cmd" in
url)
  hash=$(echo "$1" | tr 'A-F' 'a-f'); index=$2
  if find_url "$hash" "$index"; then exit 0; fi
  docker exec "$C" curl -s -o /dev/null --max-time 30 "http://127.0.0.1:8252/api/torrent-stream/files?magnet=magnet:?xt=urn:btih:$hash" || true
  if find_url "$hash" "$index"; then exit 0; fi
  if [ -n "${REUSE_QUERY:-}" ]; then
    docker exec "$C" curl -s -o /dev/null --max-time 60 "http://127.0.0.1:8252/api/torrent-stream/reuse?$REUSE_QUERY&hash=$hash&index=$index" || true
    sleep 2
    if find_url "$hash" "$index"; then exit 0; fi
  fi
  echo "file $hash:$index is not served by the TorBox proxy (a TorBox source needs to be registered: see README)" >&2
  exit 2
  ;;
extract)
  hash=$(echo "$1" | tr 'A-F' 'a-f'); index=$2; out=$3; rate=${4:-40}
  mkdir -p "$out"
  exec 9>"/tmp/nfx-truth-read.lock"
  flock 9   # one read at a time
  wait_idle
  url=$(bash "$0" url "$hash" "$index")
  tmp=/tmp/nfxtruth-${hash:0:8}-$index
  docker exec "$C" sh -c "rm -rf $tmp && mkdir -p $tmp"
  trap 'docker exec "$C" rm -rf "$tmp" >/dev/null 2>&1 || true' EXIT
  docker exec "$C" ffprobe -v error -print_format json -show_format -show_streams "$url" > "$out/probe.json"
  py() { docker run --rm -i -v "$out:/m:ro" "$TRUTH_IMAGE" python3 -I - "$@"; }
  # Audio: the first audio stream unless AUDIO_STREAM is set; FC when the layout has a centre
  read -r astream layout channels < <(py /m/probe.json "${AUDIO_STREAM:-}" <<'PY'
import json, sys
p = json.load(open(sys.argv[1])); want = sys.argv[2]
a = [s for s in p['streams'] if s['codec_type'] == 'audio']
s = next((s for s in a if want and str(s['index']) == want), a[0])
print(s['index'], s.get('channel_layout', '') or '-', s.get('channels', 0))
PY
)
  af="aresample=16000:async=1:first_pts=0"
  case "$layout" in 3.*|4.*|5.*|6.*|7.*|*FC*) af="pan=mono|c0=FC,$af";; -) [ "$channels" -ge 6 ] && af="pan=mono|c0=FC,$af";; esac
  subs=$(py /m/probe.json <<'PY'
import json, sys
p = json.load(open(sys.argv[1]))
T = ('subrip', 'ass', 'ssa', 'webvtt', 'mov_text', 'text')
print(' '.join(('t' if s.get('codec_name') in T else 'p') + str(s['index']) for s in p['streams']
               if s['codec_type'] == 'subtitle' and (s.get('codec_name') in T or s.get('codec_name') == 'hdmv_pgs_subtitle')))
PY
)
  args=(-nostdin -v error -readrate "$rate" -reconnect 1 -reconnect_on_network_error 1 -reconnect_delay_max 5 -i "$url")
  [ "${NO_AUDIO:-0}" = 1 ] || args+=(-map "0:$astream" -af "$af" -ac 1 -c:a pcm_s16le "$tmp/audio.wav")
  for s in $subs; do
    case "$s" in
      t*) args+=(-map "0:${s#t}" -c:s webvtt "$tmp/sub_${s#t}.vtt");;
      p*) args+=(-map "0:${s#p}" -c:s copy "$tmp/sub_${s#p}.sup");;
    esac
  done
  echo "reading $url (audio stream $astream, $layout, $af; subtitle tracks: ${subs:-none})" >&2
  start=$(date +%s)
  docker exec "$C" ffmpeg "${args[@]}"
  echo "read took $(( $(date +%s) - start )) s" >&2
  docker cp "$C:$tmp/." "$out/"
  ;;
*)
  sed -n '2,22p' "$0"; exit 1;;
esac
