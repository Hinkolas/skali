# 07 — gRPC Control Plane (node-to-node)

The gRPC plane is **daemon-to-daemon** only. The master uses it to drive workers
and builders; it is never exposed to CLI/web clients (those use REST, `06`). Auth
is **mTLS** with the cluster CA (`08`).

## Why gRPC and why now

- Multi-node is a core goal, so the node-control surface is defined up front (not
  deferred). The `.proto` is essentially the `Executor` interface (`03`) on the
  wire.
- The **local node is short-circuited** — the master calls `LocalExecutor`
  directly instead of dialing itself. So gRPC carries only genuinely remote
  calls. Same interface, both paths.

## Service surface (NodeService)

The proto mirrors `Executor`. Streaming RPCs use server-streaming so logs/events
flow as channels at the Go layer.

```proto
syntax = "proto3";
package skali.node.v1;

service NodeService {
  // Health & inventory
  rpc Heartbeat(HeartbeatRequest) returns (HeartbeatReply);
  rpc Inventory(InventoryRequest) returns (InventoryReply); // actual instances on node
  rpc WatchEvents(WatchEventsRequest) returns (stream NodeEvent); // docker events → reconciler

  // Networking
  rpc EnsureNetworks(EnsureNetworksRequest) returns (EnsureNetworksReply);

  // Images
  rpc PullImage(PullImageRequest) returns (stream PullProgress);
  rpc BuildImage(BuildImageRequest) returns (stream BuildLog);   // builder role

  // Container lifecycle
  rpc RunContainer(RunContainerRequest) returns (RunContainerReply);
  rpc StopContainer(StopContainerRequest) returns (StopContainerReply);
  rpc RemoveContainer(RemoveContainerRequest) returns (RemoveContainerReply);
  rpc InspectContainer(InspectContainerRequest) returns (InstanceStatus);
  rpc StreamLogs(StreamLogsRequest) returns (stream LogLine);
}
```

Message shapes follow the `Executor` parameter structs: `RunContainerRequest`
carries the full `ContainerSpec` (image digest, env, labels incl. Traefik
routing labels, network attachments, resource limits, health check).
`BuildImageRequest` carries the **source reference** — a context-blob handle the
builder fetches from the master (`04`), or `[soon]` a git ref to clone — plus the
`target_platform`, build args, and target image/tag.

**`HeartbeatReply` reports node facts the master schedules on:** the node's
**architecture** (`amd64`/`arm64`, read from the local Docker engine), its enabled
roles, and — for builders — `build_concurrency` and current build load. This is
how the master learns which builders/runners are which arch (`nodes.arch`, `05`)
so it can match a build's `target_platform` to a capable builder and place
Instances only on matching-arch nodes (`03`, `04`).

## Direction of calls & the reconciler

- The **master is the gRPC client**; workers run the **gRPC server**. The master
  pushes desired actions and pulls inventory/events.
- `WatchEvents` lets a worker stream Docker events upward so the master's
  reconciler (`03`) reacts to container deaths/starts without polling.
- A worker that loses the master keeps its containers running; on reconnect it
  serves a fresh `Inventory`, and the reconciler re-converges.

> `[open]` Connection direction under NAT: if a worker can't accept inbound gRPC
> (NAT/firewall), we may need the worker to **dial out** to the master and hold a
> reverse stream. For 0.1.0's "trusted private network, mutually reachable"
> contract, master-dials-worker is fine. Revisit for hostile-network topologies —
> see `16`.

## Streaming model

Every streaming RPC maps to a Go `<-chan T`:

- `LocalExecutor` fills the channel straight from the Docker SDK.
- `RemoteExecutor` fills it from the gRPC stream.

The service layer is identical for local and remote because the `Executor`
signatures are transport-neutral. **Two-hop streams** (client SSE ← master ←
builder gRPC) compose by piping one channel into the next.

## Transport & security

- **mTLS** on every connection; both sides present CA-signed certs and verify
  against the cluster CA. Node identity = its cert (`08`).
- Long-lived HTTP/2 connections → handshake cost amortized to ~nothing; bulk
  encryption rides AES-NI. mTLS here is about **authentication** (the network may
  host untrusted containers), not just encryption.
- Keepalive + ret/backoff on the client; deadlines on unary RPCs; streams
  cancellable via context.
- Proto lives in `proto/`, generated code checked in or generated via `buf`
  (see `14`). Versioned package (`skali.node.v1`) so the wire format can evolve.
