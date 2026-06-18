# Quartermaster GUI

A lightweight web dashboard for [Quartermaster](https://github.com/Camfel/quartermaster),
displaying running services, their configuration, and health status.

![Dark theme dashboard](docs/screenshot.png)

## Endpoints

| Path | Description |
|------|-------------|
| `/` | Full dashboard with auto-refresh |
| `/api/status` | Raw JSON from the daemon |
| `/health` | GUI health check |

## Quick Start

```bash
# Build
make ci-build

# Run (daemon must be running)
./bin/quartermaster-gui
# → http://localhost:8090
```

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `QM_SOCKET` | `/run/quartermaster/daemon.sock` | Daemon Unix socket |
| `QM_LISTEN` | `:8090` | Listen address |

## Container

Pre-built images are published to [ghcr.io/camfel/quartermaster-gui](https://github.com/Camfel/quartermaster-gui/pkgs/container/quartermaster-gui).

```bash
# Pull and run
docker run -d \
  --user $(id -u quartermaster):$(id -g quartermaster) \
  -v /run/quartermaster/daemon.sock:/run/quartermaster/daemon.sock \
  -p 8090:8090 \
  ghcr.io/camfel/quartermaster-gui:latest

# Or build locally
make cd-build
```

> **Socket permissions:** The daemon socket is `0600` by default. Pass
> `--user` matching the `quartermaster` UID, or set the socket mode to
> `0660` and share the group.

## Makefile targets

```bash
make test       # unit tests + vet + fmt
make ci-build   # compile binary (CI gate)
make cd-build   # build Docker image
make clean      # remove bin/
```
