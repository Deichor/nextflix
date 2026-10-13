# Android TV emulator for testing the app on this PC. The screen is served through noVNC on 127.0.0.1 only.
# Tag: see scripts/emulator.sh.
FROM nextflix-android:1

ARG SYSTEM_IMAGE="system-images;android-36;android-tv;x86_64"
ENV SYSTEM_IMAGE=${SYSTEM_IMAGE}

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      xvfb x11vnc novnc websockify \
      libpulse0 libnss3 libxcomposite1 libxcursor1 libxi6 libxtst6 libxdamage1 libxrandr2 libxkbfile1 \
      libgl1 libegl1 libgbm1 libdrm2 libasound2t64 libxshmfence1 libxkbcommon-x11-0 libbsd0 \
 && rm -rf /var/lib/apt/lists/*

RUN yes | sdkmanager --licenses > /dev/null \
 && sdkmanager --install "emulator" "${SYSTEM_IMAGE}" > /dev/null \
 && rm -rf "$ANDROID_HOME/.temp"

ENV PATH=/opt/android-sdk/emulator:$PATH
COPY emulator-entry.sh /usr/local/bin/emulator-entry.sh
RUN chmod +x /usr/local/bin/emulator-entry.sh
ENTRYPOINT ["/usr/local/bin/emulator-entry.sh"]
