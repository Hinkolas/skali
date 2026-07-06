# Dev cluster (Docker-in-Docker harness)

A disposable 3-node skali cluster on one machine: `master` + `worker-1` +
`worker-2`, each container running skalid **and its own Docker engine**
(DinD), joined by one compose network that stands in for the private cluster
network. This is the standard local harness for multi-node work — enrollment,
heartbeats, and (soon) the container engine, Traefik routing, and host facts.

```sh
task cluster:up      # build image, boot postgres + master, self-enroll workers
task cluster:ps      # status (healthy = enrolled + agent serving)
task cluster:logs    # follow all node logs
task cluster:down    # destroy everything (DB, identities; keeps image caches)
```

- REST API: `http://localhost:7070` — run `task dev:web` on the host for the
  UI; log in as `dev@skali.local` / `skali-dev-password`.
- Workers self-provision on first boot: login → mint join token → `skalid
  enroll` → `skalid agent`. Container restarts reuse the identity; a
  `down`/`up` cycle is a pristine cluster.
- The master's cluster gRPC (`master:7443`) is also published to the host, so
  additional bare-process nodes can still be enrolled from outside if needed.
- Every node is `privileged` (required for DinD). The inner engines'
  `/var/lib/docker` dirs live in named volumes so image pulls stay cached
  across rebuilds; `cluster:down` wipes those too (`-v`).

When Traefik/ACME work starts, add a [Pebble](https://github.com/letsencrypt/pebble)
service here as the in-network ACME test server.
