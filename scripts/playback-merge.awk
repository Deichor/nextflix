# Merges player sessions ("P<tab><playback line>") with the server's timing lines
# ("S<tab><timing line>"): prints "<seconds><tab><order><tab><line>" for sort (playback-log.sh).
# Player events go by their own time, a batch header with its first event; timing lines are kept
# when they name a file of a session (src=, to=) within 30 min before it to 2 min after it.
function daynum(d,   y, m, dd, era, yoe, doy) {
  y = substr(d, 1, 4) + 0; m = substr(d, 6, 2) + 0; dd = substr(d, 9, 2) + 0
  if (m <= 2) y--
  era = int(y / 400); yoe = y - era * 400
  doy = int((153 * (m > 2 ? m - 3 : m + 9) + 2) / 5) + dd - 1
  return era * 146097 + yoe * 365 + int(yoe / 4) - int(yoe / 100) + doy
}
function secs(t) { return substr(t, 1, 2) * 3600 + substr(t, 4, 2) * 60 + substr(t, 7) }
function when(d, t) { return daynum(d) * 86400 + secs(t) }
function flush() {
  if (head != "") printf "%.3f\t0\t%s\n", headAt, head
  head = ""
}
$1 == "P" { # "<date> <time> <session> <client> #<seq> ..." or "... <client> <event time> pos=..."
  n = split($2, f, " ")
  sid = f[3]; at = when(f[1], f[2])
  if (!(sid in first) || at < first[sid]) first[sid] = at
  if (at > last[sid]) last[sid] = at
  if (f[5] ~ /^#/) {
    flush()
    for (i = 6; i <= n; i++) if (f[i] ~ /^src=[0-9a-f]+:[0-9]+$/) files[sid SUBSEP substr(f[i], 5)] = 1
    head = $2; headSid = sid; headAt = at
    next
  }
  for (i = 6; i <= n; i++) if (f[i] ~ /^to=[0-9a-f]+:[0-9]+$/) files[sid SUBSEP substr(f[i], 4)] = 1
  ev = when(f[1], f[5])
  if (ev > at + 3600) ev -= 86400 # the event was before midnight
  if (head != "" && headSid == sid && headAt == at) { headAt = ev; flush() }
  printf "%.3f\t1\t%s\n", ev, $2
  next
}
$1 == "S" { srv[++ns] = $2 }
END {
  flush()
  for (i = 1; i <= ns; i++) {
    l = srv[i]
    at = when(substr(l, 1, 10), substr(l, 12, 8))
    for (k in files) {
      split(k, p, SUBSEP)
      if (at < first[p[1]] - 1800 || at > last[p[1]] + 120) continue
      if (match(l, " " p[2] "([^0-9]|$)")) {
        printf "%.3f\t2\t%s server %s\n", at + 0.5, substr(l, 1, 19), substr(l, 21)
        break
      }
    }
  }
}
