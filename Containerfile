# syntax=docker/dockerfile:1

FROM oven/bun:1.2 AS web-builder
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
COPY docs/superpowers/design/tokens.css /docs/superpowers/design/tokens.css
RUN bun run build

FROM golang:1.26-bookworm AS go-builder
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN rm -rf internal/adapter/httpapi/dist && \
    cp -R /dev/null /tmp/.noop 2>/dev/null || true
COPY --from=web-builder /src/web/build ./internal/adapter/httpapi/dist
RUN touch ./internal/adapter/httpapi/dist/.gitkeep && \
    go build -trimpath -ldflags="-s -w" -o /out/max ./cmd/max

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      sqlite3 \
      tar \
      tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 max \
    && useradd --uid 10001 --gid 10001 --home-dir /data/agent-home/agy --no-create-home --shell /usr/sbin/nologin max \
    && mkdir -p /data /vault /opt/agy/bin \
    && chown -R 10001:10001 /data /vault

COPY --from=go-builder /out/max /usr/local/bin/max
COPY scripts/backup.sh /usr/local/bin/max-backup
COPY scripts/restore.sh /usr/local/bin/max-restore
RUN chmod 0755 /usr/local/bin/max /usr/local/bin/max-backup /usr/local/bin/max-restore

ENV PATH="/opt/agy/bin:/usr/local/bin:/usr/bin:/bin" \
    MAX_DATA_DIR="/data" \
    MAX_VAULT_DIR="/vault" \
    MAX_LISTEN="0.0.0.0:8787" \
    HOME="/data/agent-home/agy"

USER 10001:10001
WORKDIR /vault
EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/max", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/max"]
CMD ["serve"]
