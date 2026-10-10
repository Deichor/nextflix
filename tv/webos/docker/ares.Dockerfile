# webOS CLI (ares-*) for packaging and for talking to the TV. Built by scripts/ares.sh and scripts/build.sh.
FROM node:24-bookworm-slim
ARG CLI_VERSION=3.2.6
RUN npm install -g --no-audit --no-fund @webos-tools/cli@${CLI_VERSION} \
 && apt-get update && apt-get install -y --no-install-recommends openssh-client ca-certificates \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /work
