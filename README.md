# Xpress

**Xpress** is a Go load balancer and reverse proxy. It sits in front of a set of backend servers, accepts client traffic over HTTP (with optional TLS) or raw TCP, and forwards each request to a backend using a configurable routing algorithm.

This repository is a systems / networking project: concurrent proxying, connection pooling, consistent hashing, and operational concerns such as YAML/env configuration, a CLI, and rotating logs.

---

## Why this project exists

Typical take-home or portfolio load balancers stop at HTTP round-robin. Xpress is scoped closer to a small production edge:

| Layer | What it does |
| --- | --- |
| **L7 (HTTP/HTTPS)** | Reverse proxy with `httputil`, preserves the original `Host`, sets `X-Forwarded-*` |
| **L4 (TCP)** | Accepts TCP clients, dials a backend, splices both directions, speaks **PROXY protocol v2** so backends still see the real client address |
| **Routing** | Round robin, client IP hash, consistent hash (virtual nodes + FNV-1a) |
| **Ops** | Cobra CLI (`xpress start`), Viper config (file + `XPRESS_*` env), lumberjack log rotation |

---

## Architecture

```
                    ┌─────────────────┐
   Clients ───────► │  Xpress (CLI)   │
                    │  0.0.0.0:port   │
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
              ▼              ▼              ▼
        HTTP / TLS        TCP accept     Select backend
        reverse proxy     + pool         (algorithm)
              │              │              │
              └──────────────┼──────────────┘
                             ▼
                    Backend pool from config
                    e.g. :8000, :8001, :8002
```

**Entry point:** `main.go` — Cobra root command `xpress`, subcommand `start`.

**Core library:** `router/` — routing, HTTP/TCP servers, connection pool, config parsing, log writer.

```
lb_router/
├── main.go                 # CLI, config load, logger setup
├── config.yaml             # Sample router + backend list
├── go.mod / go.work        # Multi-module workspace (root + router)
└── router/
    ├── handler.go          # Algorithms, HTTP reverse proxy, TCP listener
    ├── config.go           # YAML mapping and algorithm/mode parsers
    ├── connection_pool.go  # TCP pool, PROXY v2, bidirectional copy
    ├── log_rotation.go     # Size/age rotation + current.log symlink
    └── interface.go        # Connection interface (placeholder)
```

---

## Features (implemented)

### Proxy modes

- **HTTP** — `http.Server` + `httputil.ReverseProxy` (`Rewrite` + `SetXForwarded`).
- **HTTPS** — same path when cert and key paths are supplied to `StartHTTPServer`.
- **TCP** — `net.ListenTCP`, one goroutine per client, 10s request timeout, idle connection pool, 32KB `sync.Pool` copy buffers, half-close via `TCPConn.CloseWrite`.

### Routing algorithms

Configured in YAML as `router.algorithm`:

| Config value | Behavior |
| --- | --- |
| `round_robin` | Atomic counter, lock-free wrap around the URL list |
| `ip_hash` | FNV-1a of client IP → fixed backend (session affinity) |
| `consistent_hash` | Ring of **150 virtual nodes** per backend; lookup by client IP; wrap-around if hash is past the last vnode |

Client IP is taken from `RemoteAddr` and stored on the request context as `client_ip`. Missing IP falls back to round robin.

`LeastConnection` and `LeastResponseTime` exist as types in code but are **not wired** in `SelectServer` yet (they currently fall through to round robin).

### Configuration

Viper loads YAML from `--config`, `./config.yaml`, or `/etc/xpress/`. Environment prefix: `XPRESS` (example: `XPRESS_ROUTER_MODE=tcp`).

Sample `config.yaml`:

```yaml
server:
  port: "8080"

router:
  mode: "http"              # http | tcp
  algorithm: "round_robin"  # round_robin | ip_hash | consistent_hash
  urls:
    - "http://127.0.0.1:8000"
    - "http://127.0.0.2:8001"
    - "http://127.0.0.3:8002"
```

CLI flags:

```text
xpress start -c ./config.yaml -p 8080
```

Default listen port is **8080** (`-p`). Bind address is `0.0.0.0`.

### Observability

- **zerolog** for structured logs (intended file + pretty console).
- **lumberjack**: rotate at 10 MB, keep 5 backups, 30 days, gzip.
- Active file `./logs/xpress.log`, symlink `./logs/current.log`.

HTTP server also shuts down when the handler context is cancelled (5s shutdown timeout).

---

## Tech stack

| Piece | Choice |
| --- | --- |
| Language | Go 1.27 |
| CLI | [spf13/cobra](https://github.com/spf13/cobra) |
| Config | [spf13/viper](https://github.com/spf13/viper) |
| Logging | [rs/zerolog](https://github.com/rs/zerolog) + [lumberjack](https://github.com/natefinch/lumberjack) |
| PROXY protocol | [pires/go-proxyproto](https://github.com/pires/go-proxyproto) |
| Concurrency | goroutines, `sync.WaitGroup`, `atomic`, `sync.Pool`, buffered connection channel |

---

## How to run

**Requirements:** Go 1.27+ (see `go.mod`).

```bash
# from repository root
go run . start --config ./config.yaml --port 8080
```

Or build:

```bash
go build -o xpress .
./xpress start -c config.yaml -p 8080
```

Point backends at the URLs in `config.yaml` (or change them to real services). Send HTTP traffic to `http://localhost:8080/`.

Workspace: `go.work` includes `.` (`module lb_router`) and `./router` (`module router`).

---

## Design notes (what to discuss in an interview)

1. **Round robin without a mutex** — `atomic.AddUint64` so concurrent HTTP handlers do not serialize on a lock just to pick a backend.
2. **Consistent hashing** — virtual nodes reduce imbalance when backends are few; FNV-1a is cheap for IP keys; binary search on the sorted ring.
3. **HTTP vs TCP** — L7 uses Go’s reverse proxy (headers, Host rewrite). L4 copies bytes and prepends PROXY v2 so Layer-7 apps behind TCP still get the client IP.
4. **Pool + buffer reuse** — idle TCP conns in a bounded channel; copy buffers from `sync.Pool` to cut allocations on the hot path.
5. **Config as an ops surface** — file + env + flags, same pattern as real services.

---

## Current status and known gaps

This is an **active / incomplete** implementation, not a drop-in replacement for Nginx or HAProxy. Honest gaps:

- Least-connection and least-response-time algorithms are declared, not selected.
- TCP `StartTCPServer` currently constructs the connection pool with the **listen address**, not a backend chosen by `SelectServer` — HTTP path is the complete L7 balancer; TCP pooling is the building block.
- `start` always passes empty TLS paths, so HTTPS is in the API but not enabled from the CLI yet.
- Logger in `initLogger` is constructed but not assigned to `log.Logger` / returned, so file rotation may not be the active global logger yet.
- No health checks, retries, circuit breaking, metrics endpoint, or tests in-tree.
- `router/interface.go` is a stub.

These are natural next steps if you want to extend the project.

---

## Skills this demonstrates

Go concurrency, `net`/`net/http`, reverse proxies, L4 vs L7, hashing for affinity, PROXY protocol, CLI/config for operators, log rotation.

---

## License

Not specified in the repository. Add a license if you open-source this for applications.
