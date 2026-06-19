# Quartermaster GUI — Makefile
#   make test      → unit tests (CI gate)
#   make ci-build  → compile binary (CI verification, no Docker)
#   make cd-build  → build + tag container image for CD

.PHONY: test ci-build cd-build fmt vet clean

IMAGE      ?= ghcr.io/camfel/quartermaster-gui
TAG        ?= latest
BIN        ?= bin/quartermaster-gui
VERSION    ?= $(shell cat VERSION 2>/dev/null || echo dev)
LDFLAGS    ?= -s -w -X main.version=$(VERSION)

# ── Test ─────────────────────────────────────────────────────────────────
test: fmt vet
	go test -v -count=1 ./...

# ── CI build (binary only, no container) ─────────────────────────────────
ci-build: fmt vet test
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) .
	@echo "✓ Binary: $(BIN) (v$(VERSION))"

# ── CD build (container image) ───────────────────────────────────────────
cd-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(TAG) .
	@echo "✓ Image: $(IMAGE):$(TAG) (v$(VERSION))"

# ── Utilities ────────────────────────────────────────────────────────────
fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -rf bin/
