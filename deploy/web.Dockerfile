# Build context: web/ (see deploy/docker-compose.yml)
# Base images are pinned to exact versions; keep them in sync with scripts/check-frontend.sh
FROM node:24.21.0-alpine3.24 AS builder

WORKDIR /app
COPY package*.json ./
RUN npm ci

COPY . .

ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
# Next.js bakes these in at build time: server-side calls and /api rewrites use the internal server address
ARG BACKEND_INTERNAL_URL=http://server:8252
ENV BACKEND_INTERNAL_URL=$BACKEND_INTERNAL_URL NEXT_PUBLIC_API_URL=$BACKEND_INTERNAL_URL

RUN npm run build

FROM node:24.21.0-alpine3.24 AS runner

WORKDIR /app

ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1

RUN addgroup --system --gid 1001 nodejs && \
    adduser --system --uid 1001 nextjs

COPY --from=builder /app/public ./public
COPY --from=builder --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=builder --chown=nextjs:nodejs /app/.next/static ./.next/static

USER nextjs

EXPOSE 3008

ENV PORT=3008
ENV HOSTNAME="0.0.0.0"

HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:3008/api/health || exit 1

CMD ["node", "server.js"]
