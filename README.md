# karbiter

A minimal Kubernetes Lease API server for external leader election.

Single binary, zero dependencies, ~3MB. Implements just enough of the `coordination.k8s.io/v1` Lease API for standard client-go leader election to work against it.

## Use Case

Run on a separate host (router, NAS, Pi) as a neutral arbiter for leader election between two or more Kubernetes-compatible nodes. Any application using client-go's `leaderelection` package works unmodified.

## Design

- **Token = namespace** — each Bearer token gets an isolated lease space. No registration, no passwords. Your token is your private partition.
- **Multi-tenant** — one karbiter instance serves up to 65536 independent clusters.
- **Self-cleaning** — namespaces expire after 60 seconds of inactivity. Active clusters renew every ~10s, so they never expire. Abandoned ones vanish automatically.
- **Stateless** — all state is in memory. If karbiter restarts, leases expire naturally and clients re-elect. This is by design.
- **Safe to expose publicly** — namespace cap prevents resource exhaustion. Token isolation prevents interference between tenants.

## Usage

```bash
# Plain HTTP (LAN)
karbiter --addr :9443

# With TLS
karbiter --addr :9443 --cert server.crt --key server.key
```

No configuration needed. No accounts to create. Just start it.

## Client Configuration

Point a kubeconfig at karbiter. The token is your namespace — pick any unique string:

```yaml
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://router:9443
    insecure-skip-tls-verify: true
  name: karbiter
contexts:
- context:
    cluster: karbiter
    user: karbiter
  name: karbiter
current-context: karbiter
users:
- name: karbiter
  user:
    token: my-cluster-unique-id
```

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Health check — returns `ok {active}/{max}` |
| GET | `/apis/coordination.k8s.io/v1/namespaces/{ns}/leases/{name}` | Get lease |
| POST | `/apis/coordination.k8s.io/v1/namespaces/{ns}/leases/{name}` | Create lease |
| PUT | `/apis/coordination.k8s.io/v1/namespaces/{ns}/leases/{name}` | Update lease (optimistic concurrency) |
| GET | `/api`, `/apis`, `/apis/coordination.k8s.io/v1` | Discovery (client-go compatibility) |

## Container

```bash
docker build -t karbiter .
docker run -p 9443:9443 karbiter
```

## OpenWrt

```bash
# Cross-compile
GOOS=linux GOARCH=mipsle CGO_ENABLED=0 go build -ldflags="-s -w" -o karbiter .

# Deploy
scp karbiter root@router:/usr/bin/
```

See `openwrt/` directory for a proper package with procd init script.

## Resource Usage

| Namespaces | Memory overhead |
|-----------|----------------|
| 0 (idle) | ~11 MB (Go runtime) |
| 5,000 | ~21 MB |
| 65,536 (max) | ~140 MB |

Namespaces auto-expire after 60s of no activity. Memory returns to baseline.

## How It Works

Each Bearer token creates an isolated namespace. Within that namespace, leases are identified by `{namespace}/{name}` from the URL path. Optimistic concurrency via `resourceVersion` prevents split-brain — only the current holder (who knows the current version) can renew.

```
Client A (token: "prod-cluster") → POST .../leases/leader → creates lease, gets rv=1
Client A → PUT .../leases/leader (rv=1) → renews, gets rv=2
Client B (token: "prod-cluster") → PUT .../leases/leader (rv=1) → 409 Conflict (stale)
Client C (token: "other-cluster") → GET .../leases/leader → 404 (isolated)
```

## Build

```bash
go build -o karbiter .
```
