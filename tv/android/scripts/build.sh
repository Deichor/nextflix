#!/usr/bin/env bash
# Builds the debug and the signed release APK in a container and copies them to
# out/nextflix-tv-<version>.apk (release) and out/nextflix-tv-<version>-debug.apk.
# The release key is created on the first build in .signing/ (gitignored); keep it, an APK signed
# with another key cannot update an installed one. Version: nextflix.versionName in gradle.properties.
# The store password is only read inside the containers (never on a command line or in `docker inspect`).
# From Windows: wsl -d Ubuntu-24.04 -- bash /mnt/c/deichor-projects/nextflix/tv/android/scripts/build.sh
set -euo pipefail
source "$(dirname "$0")/common.sh"
ensure_image
VERSION="$(app_version)"
mkdir -p "$DIR/out" "$DIR/.signing"

# Inside a container: exports the password from the mounted .signing/signing.properties
READ_PASS='export NEXTFLIX_STORE_PASSWORD="$(sed -n "s/^storePassword=//p" /signing/signing.properties)"'

# Local release key: random passwords in .signing/signing.properties, created once
if [ ! -f "$DIR/.signing/release.jks" ]; then
  (umask 077 && printf 'storePassword=%s\nkeyAlias=nextflix\n' "$(od -An -tx1 -N18 /dev/urandom | tr -d ' \n')" \
    > "$DIR/.signing/signing.properties")
  docker run --rm -v "$DIR/.signing:/signing" "$IMAGE" bash -c "$READ_PASS && keytool -genkeypair -noprompt \
    -keystore /signing/release.jks -storetype PKCS12 \
    -storepass:env NEXTFLIX_STORE_PASSWORD -keypass:env NEXTFLIX_STORE_PASSWORD \
    -alias nextflix -keyalg RSA -keysize 3072 -validity 10000 -dname 'CN=Nextflix TV, O=Nextflix'" >/dev/null
  echo "Created the release key in tv/android/.signing/ (keep it)."
fi

exec 9>"$LOCK_FILE"
flock 9
docker run --rm --memory=3500m \
  -v "$DIR:/src:ro" \
  -v "$WORK_VOLUME:/work" \
  -v "$GRADLE_HOME_VOLUME:/root/.gradle" \
  -v "$DIR/.signing:/signing:ro" \
  -v "$DIR/out:/out" \
  -e GRADLE_OPTS="-Xmx128m" \
  -e ANDROID_USER_HOME=/root/.gradle/android-user \
  -e NEXTFLIX_KEYSTORE=/signing/release.jks \
  -e NEXTFLIX_KEY_ALIAS=nextflix \
  -e VERSION="$VERSION" \
  -w /work "$IMAGE" \
  bash -c "$READ_PASS && $SYNC_CMD && gradle --no-daemon --console=plain --warning-mode=none :app:assembleDebug :app:assembleRelease \
    && rm -f /out/*.apk \
    && cp app/build/outputs/apk/release/app-release.apk /out/nextflix-tv-\$VERSION.apk \
    && cp app/build/outputs/apk/debug/app-debug.apk /out/nextflix-tv-\$VERSION-debug.apk"
ls -l "$DIR"/out/*.apk
