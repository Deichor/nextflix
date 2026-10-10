# Build context: server/ (see deploy/docker-compose.yml)
FROM golang:1.27-trixie AS builder

# go-sqlite3 needs cgo
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build -ldflags '-s -w -linkmode external -extldflags "-static"' \
    -o /out/nextflix ./cmd/nextflix

FROM debian:trixie-slim

# ffmpeg remuxes torrent streams to fMP4; sqlite3 is kept for debugging the database
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    sqlite3 \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

# UID/GID 999 match the user of the previous image, so migrated volumes keep working
RUN groupadd -r -g 999 nextflix && \
    useradd -r -u 999 -g nextflix -m -d /home/nextflix nextflix && \
    mkdir -p /app/data && chown -R nextflix:nextflix /app

WORKDIR /app

COPY --from=builder /out/nextflix /app/nextflix

USER nextflix

EXPOSE 8252

HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8252/api/health > /dev/null || exit 1

CMD ["/app/nextflix"]
