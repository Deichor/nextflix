"""Subtitle streams of a probe.json (media.sh): one line per stream, "<index> <codec> <language> <sdh>"."""
import json
import sys

p = json.load(open(sys.argv[1]))
for s in p["streams"]:
    if s.get("codec_type") == "subtitle":
        tags = s.get("tags") or {}
        title = (tags.get("title") or "").lower()
        sdh = int("sdh" in title or (s.get("disposition") or {}).get("hearing_impaired", 0) == 1)
        print(s["index"], s.get("codec_name"), tags.get("language", "und"), sdh)
