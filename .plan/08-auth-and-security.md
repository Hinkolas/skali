# 08 — Auth & Security

Two independent auth systems for the two planes (`01`):

- **User auth** (REST, client plane): opaque tokens.
- **Node auth** (gRPC, control plane): mTLS via a cluster CA + one-time
  enrollment tokens.

## User auth — opaque tokens (not JWT)

Topology: one master holding the source-of-truth DB, many client devices. That
makes **opaque, server-stored tokens** the right call, not JWT.

- A token is a random secret; the DB stores only its **hash** (`tokens.hash`).
- **Web UI:** the same token delivered in an **httpOnly, Secure, SameSite**
  cookie. **CLI:** the token stored in `~/.config/skali/` (see `09`).
- **Why not JWT:** JWT's only real win is stateless verification across services
  without a shared DB — which we don't need (one master, one DB). Opaque tokens
  give **instant revocation** (`logout`, revoke a device, kill a leak) with one
  DB write — important for a tool that deploys to servers. JWT would cost us
  revocation/rotation pain for a benefit we can't use.
- **Token type is independent of acquisition flow.** 0.1.0 ships basic login
  (POST credentials → token). The `[future]` GitHub-CLI/Claude-Code-style browser
  login just mints the **same** opaque token via a different flow — no client
  rewrite, no token-format change.
- Passwords hashed with **argon2id**. Optional token `expires_at` (CLI PATs can
  be long-lived; web sessions shorter). `last_used` tracked for device lists.

### RBAC

0.1.0 ships a single `admin` user/role (the `users.role` column exists as a
stub). **[soon]** real RBAC (owner/admin/deployer/viewer, per-project scoping)
layers on the same token system — the token resolves to a user, the user to
roles, checked in chi middleware.

## Node auth — cluster CA + enrollment + mTLS

### The CA

- On first `skalid start` with the `master` role, the daemon generates a
  **cluster CA** (self-signed) and stores it in `cluster_ca` (key **encrypted at
  rest**, `05`). The CA is the root of trust for all node identities.

### Enrollment (join-token + mTLS bootstrap)

```
1. Operator mints a one-time join token on the master:
     skali nodes join-token --roles worker         (or POST /nodes/join-token)
   → random secret, stored hashed in join_tokens with an expiry + roles.
2. On the new node:
     skalid enroll --master <addr> --token <token>
   → the worker generates its own keypair + CSR, sends CSR + token to the
     master's bootstrap endpoint (server-auth TLS only at this point — no client
     cert yet).
3. Master validates the token (unused + unexpired), signs the worker's cert with
   the cluster CA, returns the signed cert + CA cert, records the node + cert
   serial, and BURNS the token (used_at set).
4. Steady state: every gRPC call is mTLS — both sides present CA-signed certs and
   verify against the CA. Worker identity = its cert. Revoke a node = revoke its
   cert / mark node disabled.
```

This is the k3s/Nomad/Teleport agent-bootstrap pattern. The join token is the
one-time bootstrap secret; mTLS is steady-state auth. They compose; neither
replaces the other.

- **Cert lifecycle:** short-to-medium-lived node certs with renewal over the
  existing mTLS channel before expiry (`[soon]`); revocation list / node-disable
  honored by the master's gRPC server.

## Threat model & defense in depth

The defining fact: **we run untrusted user code (deployed containers) inside the
private network.** So the "trusted network" is trusted *between operators/nodes*,
but it *contains hostile workloads by design*.

| Layer | Protects against | Mechanism |
|-------|------------------|-----------|
| Network contract | outside attackers on the wire | operator-provided private network |
| Control-plane mTLS | a hostile container / breached host issuing fleet commands | CA-signed node certs, verified per connection; no replayable bearer secret on the wire |
| Container firewalling (`DOCKER-USER`) | containers reaching the control plane / cluster mgmt | drop container-subnet → host control ports & cluster net (always on) |
| Network-per-app | lateral movement between tenants' apps | isolated Docker networks + shared proxy net |
| Egress policy | exfiltration from locked-down apps | `internet`/`isolated` per project |
| Secrets at rest **[soon]** | stolen DB file | **deferred out of 0.1.0** — plaintext in the DB for now (protect via host + file permissions); AEAD keyed by an operator-provided master key in a later security pass |
| User tokens | unauthorized client actions | opaque, hashed, revocable; RBAC `[soon]` |
| Build secrets | secrets baked into image layers | BuildKit `--secret` (`[soon]`) |

Mental rule (restated): the private network protects you from outsiders;
control-plane mTLS + container firewalling protect you from the code you
deliberately run inside it.

## Data-plane encryption (optional, deferred)

Internal data-plane hops (edge→node→container) are plain HTTP by default, trusting
the private network. Because every node already has a CA-signed cert, **mTLS
between proxies is a cheap `[future]` toggle** for operators who don't fully trust
their "private" network. Kept in mind, not built in 0.1.0.
