# ── Build stage ──────────────────────────────────────────────────────────
# Wolfi-based Go toolchain — zero CVEs, no shell/git/package-manager.
FROM cgr.dev/chainguard/go AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY . .
RUN ["go", "build", "-trimpath", "-ldflags=-s -w", "-o", "/quartermaster-gui", "."]

# ── Runtime stage ────────────────────────────────────────────────────────
# Wolfi-based, zero known CVEs, non-root by default (UID 65532).
FROM cgr.dev/chainguard/static:latest
WORKDIR /
COPY --from=build /quartermaster-gui /quartermaster-gui
EXPOSE 8090
ENTRYPOINT ["/quartermaster-gui"]
