#!/usr/bin/env bash
# Compiles the Android TV app in a container. Usage: [WORK_ID=<agent>] check.sh [gradle tasks...]
# Default task: assembleDebug. Example: WORK_ID=player check.sh :feature:player:compileDebugKotlin
# The sources are mounted read-only and copied into the volume nextflix-android-work-<WORK_ID>, so every
# WORK_ID builds incrementally in its own dirs. Only one Gradle build runs at a time (flock).
# Prints only compiler errors and failed tasks (WARNINGS=1: Kotlin warnings too), then GRADLE_EXIT=<code> last.
# From Windows: wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/check.sh
set -uo pipefail
source "$(dirname "$0")/common.sh"
ensure_image

TASKS=("$@")
[ ${#TASKS[@]} -eq 0 ] && TASKS=(assembleDebug)
LOG="$(mktemp /tmp/nextflix-android-check.XXXXXX)"
trap 'rm -f "$LOG"' EXIT

exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  echo "Waiting for another Gradle build ($LOCK_FILE)..." >&2
  flock 9
fi

docker run --rm --memory=3500m \
  -v "$DIR:/src:ro" \
  -v "$WORK_VOLUME:/work" \
  -v "$GRADLE_HOME_VOLUME:/root/.gradle" \
  -e GRADLE_OPTS="-Xmx128m" \
  -e ANDROID_USER_HOME=/root/.gradle/android-user \
  -w /work "$IMAGE" \
  bash -c "$SYNC_CMD && gradle --no-daemon --console=plain --warning-mode=none $(printf '%q ' "${TASKS[@]}")" > "$LOG" 2>&1
rc=$?
exec 9>&-

if [ $rc -eq 0 ]; then
  # WARNINGS=1: also the Kotlin warnings of the modules that were compiled
  [ "${WARNINGS:-0}" = 1 ] && grep -E '^w: ' "$LOG" | sed -e 's#file:///work/#tv/android/#g' | awk '!seen[$0]++' | head -80
  grep -E '^BUILD SUCCESSFUL' "$LOG"
else
  # Compiler errors (Kotlin e:, Java / AAPT error:), failed tasks and Gradle's "What went wrong" blocks;
  # paths shown relative to the repo
  awk '
    /^\* What went wrong:/ { block = 1; print; next }
    /^\* Try:/ { block = 0; next }
    block && NF { print; next }
    /^e: |^ERROR|error:|^> Task .* FAILED$|^BUILD FAILED/ { print }
  ' "$LOG" \
    | sed -e 's#file:///work/#tv/android/#g' -e 's#/work/#tv/android/#g' \
    | awk '!seen[$0]++' | head -80
  grep -qE '^e: |FAILED|What went wrong' "$LOG" || tail -30 "$LOG"
fi
echo "GRADLE_EXIT=$rc"
exit $rc
