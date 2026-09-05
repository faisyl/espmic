# syntax=docker/dockerfile:1
# Multi-stage build: static pure-Go binary (no cgo) -> minimal Alpine runtime,
# non-root. The stack already has zero cgo, so CGO_ENABLED=0 gives a static
# binary that runs on a scratch/distroless-grade image.
FROM golang:1.26 AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/espmic-server ./cmd/server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata openssl \
    && addgroup -S espmic && adduser -S -G espmic espmic

WORKDIR /app
COPY --from=builder /out/espmic-server /app/espmic-server
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

# Runtime data: SQLite DB (ESPMIC_DB_PATH) + recordings.
RUN mkdir -p /data/recordings && chown -R espmic:espmic /data

USER espmic

# HTTP management API (spec §15)
EXPOSE 8080
# control TLS/TCP (spec §7)
EXPOSE 9000

VOLUME ["/data"]

ENV ESPMIC_HTTP_ADDR=":8080" \
    ESPMIC_CONTROL_ADDR=":9000" \
    ESPMIC_DB_PATH="/data/espmic.db"

ENTRYPOINT ["/app/docker-entrypoint.sh"]