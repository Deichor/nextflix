# Shared by the scripts in this folder (sourced, not run)
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="nextflix-android:1"
# One Gradle user home (dependency cache) for every build; Gradle locks it itself
GRADLE_HOME_VOLUME="nextflix-android-gradle"
# Only one Gradle build at a time: WSL has little free RAM
LOCK_FILE="/tmp/nextflix-android-gradle.lock"
# Per-agent copy of the sources and build dirs (incremental builds, nothing shared)
WORK_ID="${WORK_ID:-main}"
case "$WORK_ID" in *[!a-zA-Z0-9_-]*|'') echo "WORK_ID may only contain letters, digits, - and _" >&2; exit 2 ;; esac
WORK_VOLUME="nextflix-android-work-$WORK_ID"

ensure_image() {
  if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    docker build -t "$IMAGE" -f "$DIR/docker/android.Dockerfile" "$DIR/docker" >&2
  fi
}

# Copies the read-only source mount into the work volume (build outputs and caches stay)
SYNC_CMD='rsync -a --delete --exclude=build/ --exclude=.gradle/ --exclude=/out/ --exclude=/.adb/ --exclude=/.signing/ --exclude=/.kotlin/ --exclude=local.properties /src/ /work/'

# Version from gradle.properties (the single place)
app_version() { sed -n 's/^nextflix.versionName=//p' "$DIR/gradle.properties" | tr -d '\r '; }
