# Playwright with Google Chrome (H.264 / AAC like the TV), for scripts/smoke.sh
ARG PW_VERSION=1.63.0
FROM mcr.microsoft.com/playwright:v${PW_VERSION}-noble
ARG PW_VERSION
WORKDIR /pw
RUN npm init -y >/dev/null && npm i --no-audit --no-fund --loglevel=error playwright@${PW_VERSION} \
 && npx playwright install chrome
