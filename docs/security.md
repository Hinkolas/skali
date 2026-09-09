# Security boundaries and key recovery

## Trusted operators and workloads

The alpha is intended for a team running code it trusts. Project and environment
roles control API actions, including deploys and credential access. They do not
provide a security boundary between hostile application containers.

Application pods share cluster networking. skali does not yet generate default
deny network policies for every environment, enforce a restricted pod-security
profile, or disable service-account token mounting across all workloads. Selected
platform services have network policies, but those do not isolate every tenant.
Treat permission to deploy as permission to run code on the shared cluster.
Do not offer arbitrary untrusted tenants access to this alpha.

## Preserve the encryption key

`AUTH_SECRET` encrypts TOTP secrets, environment values, and backup-target
credentials stored in the system database. Generate it once per installation
and keep a secure recovery copy alongside the system database and installation
record. A database backup alone is insufficient to recover these encrypted
records. Environment backups do not include skali's system database or its key.

Managed installations store the key in the `skali-auth` Secret in
`skali-system`. Export it only to a protected backup destination, alongside
`/var/lib/skali/installation.yaml`; do not put it in Git, logs, tickets, or shell
history. All processes that read encrypted system-database records must use the
same original key.

Changing `AUTH_SECRET` does not rotate existing ciphertext. There is no supported
key-rotation command yet. If it is accidentally replaced, restore the original
key before resuming writes. If the old key is lost, the encrypted records cannot
be recovered by resetting a password or reinstalling skali. If a key was
compromised, recovery also requires replacing the affected external credentials;
do not mistake a new encryption key for credential revocation.

## Reverse proxies and client addresses

The API uses the socket peer for IP-based limits and session addresses unless
that peer is trusted. Kubernetes-connected daemons discover the running Traefik
pods in `kube-system` and console pods in `skali-system`; application pods and
host-network pods are excluded. Discovery refreshes every five seconds, clears
on failure, and expires after ten seconds. During startup or an API outage,
requests remain available but limits may temporarily group users by proxy IP.

For an operator-managed proxy, set `SKALI_TRUSTED_PROXIES` to its exact IPv4/IPv6
CIDRs, separated by commas. The default adds no static ranges. For a local BFF,
`127.0.0.1/32,::1/128` is sufficient. Never allow the entire pod network or
`0.0.0.0/0`: that would let application code invent its client address again.
Trusted proxies must overwrite `X-Real-IP` or append the actual socket peer to
`X-Forwarded-For`; the API walks the latter from right to left and stops at the
first untrusted hop. Invalid addresses fall back to the socket peer.

The managed console trusts one Traefik hop (`ADDRESS_HEADER=x-forwarded-for`,
`XFF_DEPTH=1`). Its ingress NetworkPolicy permits port 3000 only from Traefik
pods in `kube-system`. Keep the k3s network-policy controller enabled and do not
add a broader allow policy or expose this pod directly. Node administrators
remain trusted. With a custom console deployment, enforce the same ingress
restriction before enabling forwarded address headers. See the
[SvelteKit proxy configuration](https://svelte.dev/docs/kit/adapter-node#Environment-variables-ADDRESS_HEADER-and-XFF_DEPTH)
and [Kubernetes NetworkPolicy semantics](https://kubernetes.io/docs/concepts/services-networking/network-policies/).

## Secret scanning

CI runs pinned Gitleaks against the full Git history, including on release tags.
`.gitleaks.toml` retains the default rules and permits only reviewed synthetic
fixture values at exact paths. Tests are not excluded wholesale. When changing a
fixture, use an obviously fake value; never add an exception for a real key.
Run `gitleaks git --redact --config .gitleaks.toml --log-opts=--all .` locally
with Gitleaks 8.30.1. This checks commits; review uncommitted changes before
committing and never treat a clean automated scan as proof of no secrets.
