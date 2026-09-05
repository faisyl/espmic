# ESPMIC server build helpers
.PHONY: docker-build docker-build-release build test vet

# Default version info (overridden by make arguments or CI env)
BUILD_VERSION ?= dev
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Build the docker image with git commit stamped in the binary.
# Usage: make docker-build [GIT_COMMIT=abc123] [BUILD_DATE=...] [BUILD_VERSION=...]
docker-build:
	docker compose build \
		--build-arg BUILD_VERSION=$(BUILD_VERSION) \
		--build-arg GIT_COMMIT=$(GIT_COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE)

# One-liner for local dev: stamp current git commit + timestamp
docker-build-dev:
	GIT_COMMIT=$$(git rev-parse --short HEAD) \
	BUILD_DATE=$$(date -u +%Y-%m-%dT%H:%M:%SZ) \
	docker compose build

# Build static binary locally (pure Go, CGO_ENABLED=0)
build:
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X main.version=$(BUILD_VERSION) -X main.commit=$(GIT_COMMIT) -X main.date=$(BUILD_DATE)" -o espmic-server ./cmd/server

# Run tests
test:
	go test -race ./...

# Vet code
vet:
	go vet ./...

# Build + test + vet
check: build test vet