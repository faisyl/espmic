#!/bin/sh
# docker-entrypoint.sh — run as the non-root `espmic` user (Dockerfile USER).
# Generates a self-signed TLS cert/key pair on first start if both env vars
# are set and either file is missing; otherwise reuses existing files. Final
# line execs the server binary.
set -eu

: "${ESPMIC_TLS_CERT:=/data/certs/cert.pem}"
: "${ESPMIC_TLS_KEY:=/data/certs/key.pem}"
: "${ESPMIC_TLS_CN:=espmic.local}"

if [ -n "$ESPMIC_TLS_CERT" ] && [ -n "$ESPMIC_TLS_KEY" ]; then
    if [ -f "$ESPMIC_TLS_CERT" ] && [ -f "$ESPMIC_TLS_KEY" ]; then
        echo "[entrypoint] TLS cert/key already present — skipping generation"
    else
        echo "[entrypoint] generating self-signed TLS cert (CN=$ESPMIC_TLS_CN)"
        mkdir -p "$(dirname "$ESPMIC_TLS_CERT")" "$(dirname "$ESPMIC_TLS_KEY")"
        openssl req -x509 -newkey rsa:2048 -nodes \
            -keyout "$ESPMIC_TLS_KEY" \
            -out "$ESPMIC_TLS_CERT" \
            -days 3650 \
            -subj "/CN=$ESPMIC_TLS_CN" \
            -addext "subjectAltName=DNS:$ESPMIC_TLS_CN" \
            2>/dev/null
        chmod 600 "$ESPMIC_TLS_KEY"
        echo "[entrypoint] wrote $ESPMIC_TLS_CERT + $ESPMIC_TLS_KEY"
    fi
fi

exec /app/espmic-server "$@"
