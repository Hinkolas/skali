# Security boundaries and key recovery

## Trusted operators and workloads

The alpha is intended for a team running code it trusts. Project and environment
roles control API actions, including deploys and credential access. They do not
provide a security boundary between hostile application containers.

Environments are isolated on the network by default. Every environment
namespace carries an ingress default-deny policy: a pod admits connections
only from the pods of its own environment and from the edge (which serves the
declared routes and validates certificates), on any port. Another environment
of the same project, another project, and the platform namespace are denied.
The platform ports are fenced the same way: database pools and the S3 gateway
admit only the environments holding a claim on them, skalid, the edge, and the
operators, so a workload cannot reach another environment's database or bucket
even with its credentials. The live test suite verifies that the embedded
network policy controller of the pinned k3s enforces these policies, on managed
installs and in `skali dev` alike.

Not covered yet: egress is unrestricted (a pod may open connections to the
internet, the cluster DNS, and any service that admits it), there is no manifest
surface to widen an environment's scope or to restrict a route to source
addresses (tracked in #93), no restricted pod-security profile is enforced, and
service-account tokens are still mounted into every workload.
Treat permission to deploy as permission to run code on the shared cluster.
Do not offer arbitrary untrusted tenants access to this alpha.

## Preserve the encryption key

`AUTH_SECRET` encrypts TOTP secrets, environment values, and backup-target
credentials stored in the system database. Generate it once per installation
and keep a secure recovery copy alongside the system database and installation
record. A database backup alone is insufficient to recover these encrypted
records. Environment backups do not include Skali's system database or its key.

Managed installations store the key in the `skali-auth` Secret in
`skali-system`. Export it only to a protected backup destination, alongside
`/var/lib/skali/installation.yaml`; do not put it in Git, logs, tickets, or shell
history. All processes that read encrypted system-database records must use the
same original key.

Changing `AUTH_SECRET` does not rotate existing ciphertext. There is no supported
key-rotation command yet. If it is accidentally replaced, restore the original
key before resuming writes. If the old key is lost, the encrypted records cannot
be recovered by resetting a password or reinstalling Skali. If a key was
compromised, recovery also requires replacing the affected external credentials;
do not mistake a new encryption key for credential revocation.

## Reverse proxies and client addresses

The API uses the socket peer for IP-based limits and session addresses unless
that peer is trusted. Kubernetes-connected daemons discover the running Traefik
pods in `kube-system`; application pods and
host-network pods are excluded. Discovery refreshes every five seconds, clears
on failure, and expires after ten seconds. During startup or an API outage,
requests remain available but limits may temporarily group users by proxy IP.

For an operator-managed proxy, set `SKALI_TRUSTED_PROXIES` to its exact IPv4/IPv6
CIDRs, separated by commas. The default adds no static ranges. For a local development proxy,
`127.0.0.1/32,::1/128` is sufficient. Never allow the entire pod network or
`0.0.0.0/0`: that would let application code invent its client address again.
Trusted proxies must overwrite `X-Real-IP` or append the actual socket peer to
`X-Forwarded-For`; the API walks the latter from right to left and stops at the
first untrusted hop. Invalid addresses fall back to the socket peer.

On the bundled k3s edge, Traefik runs as a DaemonSet on every edge node and
binds ports 80 and 443 on the node itself; its Service is a plain
ClusterIP, so k3s's ServiceLB, which rewrites the source of every
connection it forwards, is not in the path. The address the API resolves,
and the one applications receive, is the visitor's when the visitor
connects to an edge node directly, through any of the node's addresses
(public, private, or a floating IP). Only edge nodes answer on 80 and 443;
keep DNS and load balancers pointed at them. Because a host port cannot be
held twice, an update restarts each edge node's Traefik in place, one node
at a time, for about a second; a load balancer's health checks route
around it.

A TCP load balancer in front of the edge nodes becomes the peer instead.
Declare its addresses, and Traefik reads the visitor's address from the
balancer's PROXY protocol header on ports 80 and 443:

```yaml
# init.yaml
edge:
  proxyProtocol:
    trustedIPs: [10.0.0.5]   # the balancer's address as the nodes see it
```

or `sudo skali cluster init --proxy-protocol-trusted-ips 10.0.0.5` on a
server node of an initialized cluster (`none` turns it off; omitting the
flag or the block keeps the recorded choice). Use the address the balancer
connects from, for example its private network address when it reaches
the nodes over a private network. Only connections from the declared
addresses may carry the header; every other peer is served as itself, so a
client cannot forge an address by sending one. The cluster refuses ranges
that would trust everyone or overlap the pod and service networks
(`10.42.0.0/16`, `10.43.0.0/16`), from which application pods could forge
headers. The header is optional from a trusted balancer, so apply the
setting first and then enable PROXY protocol on the balancer for both
ports; the reverse order breaks every connection until the setting lands.

Upgrading from a release that still ran Traefik behind ServiceLB
(v0.1.4-rc.1 and older) interrupts the edge once while ServiceLB releases
the ports and Traefik takes them over, for a second or two.

## Browser sessions

The embedded static Studio calls the Go API on the same origin. Login and
2FA verification accept `session_transport: cookie`, setting a host-only
`skali_session` cookie with HttpOnly, SameSite=Lax, and Path=/. Its expiry
follows the database session's sliding expiry. The cookie response omits the
session token; CLI login defaults to a bearer-token response.

`SKALI_COOKIE_SECURE` defaults to true; both the managed installation and the
local platform terminate TLS at the edge and set it explicitly. Disable it
only for a bare `skalid` run outside any platform bundle. Browser
mutations and cookie-mode login/verification require `X-Requested-With: skali`
and Go's cross-origin protection. Credentialed CORS is not enabled. Cookies
are accepted only when Authorization is absent; a malformed or invalid header
never falls back to a cookie. Both transports share authorization and sudo gates.

Cookie-authenticated exec WebSockets require a same-origin Origin before any
session is opened. Scheme detection accepts `X-Forwarded-Proto` only from a
trusted socket peer; configure TLS-terminating proxies to overwrite it and
preserve the public Host. CLI bearer WebSockets do not require Origin.

## Edge probes reach out to route domains

The reconciler decides whether a route's domain already points at this
installation by requesting `/.well-known/skali-edge` from every address the
domain resolves to, with the domain as the Host header, from inside the
cluster. The request is a fixed-path GET on port 80 that never follows
redirects and reads response headers only; the body is never read. Whoever
operates the old host sees one small request per domain every two minutes
in their access log, identified as `skali-edge-probe/<version>`. The
addresses are whatever public DNS returns for a domain a project member
declared, so a member with deploy rights can make the daemon send that GET
to an address of their choosing; the request carries nothing about the
installation beyond its User-Agent, and the answer is compared to the
installation's own identity, never interpreted.

## Secret scanning

CI runs pinned Gitleaks against the full Git history, including on release tags.
`.gitleaks.toml` retains the default rules and permits only reviewed synthetic
fixture values at exact paths. Tests are not excluded wholesale. When changing a
fixture, use an obviously fake value; never add an exception for a real key.
Run `gitleaks git --redact --config .gitleaks.toml --log-opts=--all .` locally
with Gitleaks 8.30.1. This checks commits; review uncommitted changes before
committing and never treat a clean automated scan as proof of no secrets.
