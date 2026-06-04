# 10 — `skalid` Daemon

The server-side daemon. Plain **cobra** (no huh). One binary, behavior determined
by its assigned **roles** (`01`).

## Daemon config

`/etc/skali/skalid.yaml` (override with `--config`); data under `/var/lib/skali`.

```yaml
node:
  name: node-a
  roles: [master, edge, worker, builder]   # 0.1.0 single node = all four
  advertise_addr: 10.0.0.10                 # peer-facing private address
  public_addr: 203.0.113.10                 # edge role only
  proxy_port: 7080

api:
  listen: 0.0.0.0:7000        # REST (clients/web)
grpc:
  listen: 0.0.0.0:7001        # control plane (mTLS)

data_dir: /var/lib/skali       # sqlite, certs, CA storage
# master_key_file: /etc/skali/master.key   # [soon] — decrypts secrets at rest (08, 05)

registry:
  builtin: true               # master manages a registry:2 container (default)
  listen: 10.0.0.10:5000      # private-net bind; nodes pull over the cluster net
  # External registries are connected as data via REST/UI (a Registry record),
  # not here — e.g. a separate release registry for promotion (04).

storage:                      # volumes + the disk-safety floor (17)
  volumes_dir: /var/lib/skali/volumes  # data path for app/pool volumes — keep on a
                                       # partition ISOLATED from data_dir / host root
  reserve: 10%                # headroom skalid refuses to provision into (or 5Gi, …)
  # quota_backend: xfs        # enforcement mechanism (xfs/zfs project quota | loopback) — [impl]

databases:                    # managed engine pools (Postgres in 0.1.0), supervised like the registry
  postgres:
    image: postgres:16
    # shared pools host many logical DBs; dedicated allocation = one pool per DB (17)
    # backups: written to a backup-target volume on a schedule (17)

proxy:
  provider: traefik
  acme:
    email: ops@example.com
    # http-01 by default (no DNS creds). dns-01 provider config = [soon]

tls:
  ca_dir: /var/lib/skali/ca    # cluster CA (master); node cert/key
```

## Command tree

```
skalid
├── start                 # run the daemon (all enabled roles)
├── enroll                # join an existing cluster
│   └── --master <addr> --token <join-token>
├── nodes                 # master-side cluster admin (mirror of REST)
│   ├── list
│   └── join-token --roles worker [--ttl 1h]
├── status                # local health, roles, connectivity
├── version
└── ca                    # CA utilities (master): show / rotate [soon]
```

## `skalid start` boot sequence

1. Load config; resolve enabled roles.
2. Open SQLite (data dir); **run goose migrations** (`goose.Up`, embedded).
3. *(Secrets-at-rest decryption is **[soon]**; 0.1.0 reads plaintext — no master
   key step yet.)*
4. **master role:** ensure cluster CA exists (generate on first boot);
   start REST API (chi) + serve embedded web UI; start gRPC server; start the
   **reconciler**.
5. **edge role:** ensure Traefik is running with ACME + the HTTP provider
   pointed at the master's `/internal/traefik/http`.
6. **worker role:** connect to the local Docker socket; ensure skali networks
   exist; install `DOCKER-USER` firewall rules (`02`); ensure the node Traefik
   (Docker provider) is running; **prepare the storage path** (`storage.volumes_dir`
   + quota backend) and report free space + headroom; begin streaming Docker events
   upward / serving `Inventory`. Any **database pools / volumes** pinned to this
   node are reconciled (ensure-only, `03`/`17`) by the master once it connects.
7. **master role (registry):** ensure the managed `registry:2` container is
   running (storage under the data dir, bound to the private net, cluster-CA
   auth); register it as the default `builtin` Registry record on first boot.
8. **builder role:** verify BuildKit availability; determine `build_concurrency`.
9. Detect the node's **architecture** from the local Docker engine (`amd64`/
   `arm64`).
10. Announce readiness; begin heartbeating (arch + roles + builder load, `07`) if
    not the master, or reconciling if master.

## Managing Traefik, the registry & database pools

skalid manages its infrastructure dependencies **as containers it owns** (creates,
configures, supervises via the Docker SDK) rather than expecting the user to run
them. **The same pattern that runs Traefik and the registry runs the managed
database pools** — they are skali-owned infrastructure, not user containers:

- **Traefik** — edge and worker proxies are the same image with different enabled
  providers; skalid sets the entrypoints, ACME resolver, and provider config.
- **Registry** — a managed **`registry:2`** container on the master is a **core
  dependency, on par with Traefik** (not optional, not `[soon]`). skalid owns its
  lifecycle: storage under the data dir, cluster-CA auth, restart policy, and the
  retention/GC passes (`04`). It backs the default `builtin` Registry.
- **Database pools** — managed **`postgres`** engine containers (`17`), supervised
  the same way: skalid runs them, pins them to a node, puts their data dir on a
  **quota-enforced volume**, creates isolated logical databases + roles inside them
  on demand, and runs the **scheduled backups**. Shared pools host many tenants;
  dedicated allocation = one pool per database. Stateful, so ensure-only (`03`).
- **Volumes** — skalid provisions app/pool volumes under `storage.volumes_dir`,
  **enforces each `size_limit`**, and **reserves headroom** so a runaway can't
  exhaust the host (`17`'s disk-safety floor).
- An operator may additionally **connect an external registry** as data (a
  `Registry` record via REST/UI) — e.g. a separate release registry for promotion
  — but that's configuration, not a managed container.
- Net effect: "install skalid, run `start`" yields a working node with proxy +
  registry + the ability to provision databases and quota'd storage — no manual
  Traefik/registry/Postgres setup.

## Lifecycle & ops

- Graceful shutdown: stop accepting new work, finish in-flight reconcile steps,
  close gRPC/REST. Running app containers are left running (they outlive the
  daemon; reconciled on restart).
- Health endpoint for the daemon itself (separate from app health).
- Structured logging; `[soon]` Prometheus metrics endpoint.
- Single static binary + a systemd unit (and a `docker run` option, mounting the
  Docker socket) as the documented install methods.
