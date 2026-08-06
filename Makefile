.PHONY: run test build build-all build-target

PROJECT := banka-code
BINARY := banka
VERSION ?= 0.1.0
HOST_OS := $(shell go env GOOS)
HOST_ARCH := $(shell go env GOARCH)

run:
	go run ./cmd/banka

test:
	go test ./...

build:
	$(MAKE) build-target GOOS=$(HOST_OS) GOARCH=$(HOST_ARCH) SMOKE_TEST=1

build-all:
	$(MAKE) build-target GOOS=linux GOARCH=amd64
	$(MAKE) build-target GOOS=linux GOARCH=arm64
	$(MAKE) build-target GOOS=darwin GOARCH=amd64
	$(MAKE) build-target GOOS=darwin GOARCH=arm64
	$(MAKE) build-target GOOS=windows GOARCH=amd64
	$(MAKE) build-target GOOS=windows GOARCH=arm64

build-target:
	@set -e; \
	arch_label="$(GOARCH)"; \
	if [ "$$arch_label" = "amd64" ]; then arch_label="x64"; fi; \
	ext=""; \
	if [ "$(GOOS)" = "windows" ]; then ext=".exe"; fi; \
	output="dist/$(PROJECT)-$(GOOS)-$$arch_label/bin/$(BINARY)$$ext"; \
	mkdir -p "$$(dirname "$$output")"; \
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o "$$output" ./cmd/banka; \
	if [ "$(SMOKE_TEST)" = "1" ]; then \
		"$$output" --version | grep -F "$(VERSION)" >/dev/null; \
	fi; \
	echo "✓ $$output"
