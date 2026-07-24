# Cluster transcripts

`skali cluster` is the privileged installation and recovery command group of
the `skali` CLI. It owns host-level K3s lifecycle and the installer-owned
Skali system bundle. It never depends on the Skali API or product database,
and no other `skali` command can perform any action shown here; the boundary
is the command group and its engine package.

The root-owned installation record lives at
`/var/lib/skali/installation.yaml`; `init` also writes an in-cluster copy
that `skalid` imports on first boot. The handoff transfers observation and
bookkeeping only; mutation authority over bootstrap resources stays here.

## Version 2 staged enrollment and convergence

New managed clusters use installation-record schema 2. The first server is
the unavoidable k3s seed, but the Skali platform does not need to exist
before other hosts enroll:

```console
$ sudo skali cluster create --config node.yaml
  ok  Install k3s v1.33.3+k3s1 (server)
  ok  Bootstrap cluster coordinator

$ sudo skali cluster token --role server
one-time server invitation for cluster "production" (expires 2026-07-25T10:00:00Z):
  sudo skali cluster join <coordinator> --token-file <file> --capabilities <list>

enrollment token (write it to <file>, mode 0600):
  skali.eyJ2ZXJzaW9uIjoxLCJpbnZpdGF0aW9uIjoiLi4uIiwiY3JlZGVudGlhbCI6Ii4uLiIsImNhUGluIjoic2hhMjU2Oi4uLiJ9

The token carries authentication only; it contains no endpoint, role,
cluster name, k3s token, or registry credential.
```

The joining user supplies the coordinator location. `10.1.0.3`,
`10.1.0.3:6444`, and `https://10.1.0.3` normalize to the same HTTPS origin;
explicit HTTP is rejected. Endpoint reachability, the pinned coordinator CA,
the one-use invitation, role, requested capabilities, hostname, and node IP
are verified before the host is modified.

```console
$ sudo skali cluster join 10.1.0.3 \
    --token-file /root/invitation \
    --capabilities database,application
node db-2 enrolled in cluster "production" as server; pending cluster apply
No k3s files or services were installed.
```

Several enrollments and edits may be staged before one convergence:

```console
$ sudo skali cluster node capabilities db-2 database application
staged capabilities for db-2 in candidate revision ...
No cluster services were modified.

$ sudo skali cluster plan
cluster plan ... -> ...
add-server                db-2 -> application,database
add-agent                 app-2 -> application
reconcile-platform         reconcile platform once at database tier asynchronous

$ sudo skali cluster apply --wait
cluster operation ... accepted; target revision ...
cluster converged at revision ...
```

Servers join sequentially, agents with bounded parallelism. New nodes keep a
`skali.dev/pending` NoSchedule taint until every membership action succeeds.
The coordinator then writes final capability labels, converges the platform
once, and only afterward drains removals. A server removal takes an etcd
snapshot first. Interrupted operations retain their target and step journal;
repeating `apply` resumes the same operation ID.

`cluster init` is the first platform-enabled apply. It freezes all pending
nodes, waits for the target topology, collects the one-time admin inputs,
deploys the bundle, and advances the converged revision only after health
verification. The registry's local-volume node is recorded at this point and
cannot be removed or lose its registry capability until an explicit migration
feature exists.

Useful candidate and recovery commands:

```console
skali cluster changes import cluster-layout.yaml
skali cluster changes discard
skali cluster node remove NODE
skali cluster node restore NODE
skali cluster apply --rebalance-workloads
skali cluster rebalance --workloads
skali cluster token list
skali cluster token revoke INVITATION_ID
skali cluster node forget NODE --force
```

`status` shows converged, target, and candidate revisions, the durable
operation phase and node-step progress, agent heartbeats, and local recovery
state even when the Kubernetes API or coordinator is unavailable.

## Version 1 legacy transcripts

The remaining transcripts describe schema-version-1 clusters. They preserve
immediate k3s joins, `skali1.` and raw K10 tokens, init-owner behavior, and
per-host maintenance. New binaries route by record/token version and do not
migrate these clusters.

## 1. Interactive fresh single node

```console
$ sudo skali cluster
Skali cluster
  version  2.0.0
  k3s      v1.33.3+k3s1 (pinned)

◆ cp-1
  status     fresh
  os         Ubuntu 24.04 (linux/amd64)
  k3s        not installed
  record     none

This host is not part of a Skali installation. Install one?

◆ How should this host join Skali?
│ Create a new cluster
◆ cluster name
│ production
◆ What should this node run?
│ application, database, object-storage, registry, edge
◆ api/ui domain
│ skali.example.com
◆ registry domain
│ registry.example.com
◆ tls issuer email
│ ops@example.com

  ok  Install k3s v1.33.3+k3s1 (server)
  ok  Stamp capability labels on node cp-1
  ok  Write /var/lib/skali/installation.yaml

◆ This is the only node so far. Initialize Skali now?
│ Yes

cluster layout
  NODE   ROLE    CAPABILITIES
  cp-1   server  application, database, object-storage, registry, edge

derived topology
  database availability tier  single (1 database node)
  registry placement          cp-1 (installer-owned volume)

  ok  Enable embedded registry mirror
  ok  Apply blessed operators (CNPG, Traefik, cert-manager)
  ok  Apply bootstrap database (CNPG, 1 instance, tier single)
  ok  Apply managed registry (single instance on cp-1)
  ok  Apply skalid (wired to bootstrap credentials)
  ok  Write in-cluster installation record
  ok  Wait for skalid ready

◆ admin email
│ nicholas@example.com
◆ admin password
│ entered
  ok  Create admin account

Skali is ready:
  https://skali.example.com        api/ui
  https://registry.example.com     managed registry

Install logs: /var/lib/skali/logs/init-01J9X2.log
```

Active single-selects show `●`/`○` and use the arrow keys. Capability
multi-selects show `■`/`□`; Space toggles and Enter confirms. Each answer
then settles to the connected `◆`/`│` shape above before installation tasks
start.

The first server initializes the embedded etcd cluster (`cluster-init`),
so additional servers can join later without a datastore change. Choosing
the join path asks for the token first, derives its cluster, role, and
default endpoint, then asks only for capabilities and any desired endpoint
override.

`init` runs before `skalid` or its database exist, so its steps are logged
locally, not in the product run journal.

The managed registry requires token authentication from the start: `init`
generates the token-signing keypair (the registry trusts the certificate,
`skalid` holds the key and mints short-lived, repository-scoped tokens),
and the registry publishes on its own domain with the token realm at
`https://registry.example.com/token`. Point DNS for both domains at the
cluster; nodes must be able to resolve the registry domain themselves,
because containerd fetches pull tokens from that realm using the node
credential the installer wrote into `/etc/rancher/k3s/registries.yaml`.

## 2. Non-interactive configuration

Per-host install configuration (cloud-init, CI):

```yaml
# node.yaml (agent joining an existing cluster)
capabilities: [database]
join:
  tokenFile: /root/skali-join-token
```

```console
$ sudo skali cluster install --config node.yaml
$ echo $?
0
```

Cluster initialization configuration:

```yaml
# init.yaml
endpoints:
  api: skali.example.com
  registry: registry.example.com
tls:
  issuerEmail: ops@example.com
admin:
  email: nicholas@example.com
  passwordFile: /root/skali-admin-password
```

```console
$ sudo skali cluster init --config init.yaml
```

Version-1 `skali1` tokens supply the cluster, role, and default server URL.
Older secure tokens remain usable when `cluster`, `role`, and
`join.server` are supplied in the config. Conflicting config and token
claims are rejected before the host is changed.

Non-interactive runs take every decision from the configuration and fail
rather than prompt. An optional cluster-layout document
(`schemas/skali-layout.schema.json`) can be passed with `--layout` to assert
the expected membership; `init` refuses to proceed if the joined nodes do not
match it.

## 3. Multi-node join and initialization

On the first server:

```console
$ sudo skali cluster token
join command for cluster "production" (token expires in 24h):
  sudo skali cluster join --token-file <file> --capabilities <list>

join token (write it to <file> on the joining host, mode 0600):
  skali1.eyJrM3MiOiJLMTAuLi4iLCJwdWxsIjoiLi4uIiwicm9sZSI6ImFnZW50IiwiY2x1c3RlciI6InByb2R1Y3Rpb24iLCJzZXJ2ZXIiOiJodHRwczovL2NwLTEuaW50ZXJuYWw6NjQ0MyJ9
```

The printed token is composite: it bundles a secure `K10` k3s token, the
cluster's registry pull credential, and non-secret cluster/role/server
routing claims. A raw secure k3s token still works when those routing values
are supplied explicitly, but the node then lacks the managed-registry
credential. Short k3s tokens are refused because they cannot authenticate
the target cluster before credentials are sent.

On each additional host (joining is initiated per host; the installer never
stores SSH credentials or reaches into other machines):

```console
$ sudo skali cluster join --token-file /root/token --capabilities database
  ok  Validate https://cp-1.internal:6443 CA and agent credential
  ok  Install k3s v1.33.3+k3s1 (agent)
  ok  Join cluster "production"
  ok  Stamp capability labels on node db-1
  ok  Write /var/lib/skali/installation.yaml
```

Additional servers join with the cluster's permanent server token, because
k3s bootstrap tokens can only join agents. The token command prints the
risk and the quorum consequence, and a token minted for one role refuses
the other at join time. A server on the legacy sqlite datastore refuses to
mint server tokens; reinstalling the cluster is the path to HA there.

```console
$ sudo skali cluster token --role server
join command for cluster "production" (server token, never expires):
  sudo skali cluster join --token-file <file> --capabilities <list>

join token (write it to <file> on the joining host, mode 0600):
  skali1.eyJrM3MiOiJLMTAuLi4iLCJwdWxsIjoiLi4uIiwicm9sZSI6InNlcnZlciIsImNsdXN0ZXIiOiJwcm9kdWN0aW9uIiwic2VydmVyIjoiaHR0cHM6Ly9jcC0xLmludGVybmFsOjY0NDMifQ

warning: this is the cluster's permanent server token; it grants full
administrator access and never expires. Delete the token file on the
joining host once the join completes.

note: this join would make 2 servers; etcd quorum prefers one or three
```

```console
$ sudo skali cluster join --token-file /root/token --capabilities edge
  ok  Validate https://cp-1.internal:6443 CA and server credential
  ok  Install k3s v1.33.3+k3s1 (server)
  ok  Join cluster "production"
  ok  Stamp capability labels on node cp-2
  ok  Write /var/lib/skali/installation.yaml

warning: the cluster now has 2 servers; etcd quorum prefers one or three,
so join another server soon
```

The even-count warnings never refuse: two servers is the unavoidable step
on the way to three.

Use `skali cluster token --server https://cluster-lb.example.com:6443` to
advertise a load balancer or alternate address. A joining host may also
override the token's endpoint with `join --server`; the override is accepted
only when its CA hash and role-specific credential probe match the token.
The authenticated probe performs no node registration.

After all planned nodes have joined, once on a server:

```console
$ sudo skali cluster init --config init.yaml
cluster layout (from node labels)
  NODE    ROLE    CAPABILITIES
  cp-1    server  edge, registry
  cp-2    server  edge
  cp-3    server  edge
  app-1   agent   application, object-storage
  app-2   agent   application, object-storage
  db-1    agent   database
  db-2    agent   database

derived topology
  database availability tier  asynchronous (2 database nodes)
  registry placement          cp-1 (installer-owned volume)

  ok  Apply blessed operators
  ok  Apply bootstrap database (CNPG, 2 instances, asynchronous)
  ...
```

## 4. Availability-tier upgrade after adding a database node

After `join --role agent --capabilities database` on a new host db-3,
re-running `skali cluster` on a server detects the drift between layout and
deployed topology:

```console
$ sudo skali cluster
◆ cp-1
  status     healthy · Skali server (cluster "production")

  database nodes    3 (db-1, db-2, db-3)
  deployed tier     asynchronous
  available tier    synchronous

◆ What would you like to do?
│ Apply database tier

Upgrade the bootstrap database from asynchronous to synchronous quorum
replication. This adds a replica and briefly reconfigures replication; no
data is deleted.
◆ Apply this database tier change?
│ Yes

  ok  Scale bootstrap database to 3 instances (quorum any 1 of 2)
  ok  Verify replication state

bootstrap database tier: synchronous

Shared platform pools are skalid-owned. Upgrade them through Skali
(System page or the admin CLI); skalid now reports the higher tier as
available for its pools.
```

Tier changes never happen as a side effect of a node joining, and each owner
scales its own databases: the installer scales only the bootstrap database,
`skalid` scales its platform pools through an explicit product operation.

The non-interactive form is `sudo skali cluster tier --yes`; status marks a
pending change with a drift line (`tier deployed single, available
asynchronous (run skali cluster tier)`). The reverse direction exists for a
cluster that lost a database node; the downgrade warns instead:

```console
$ sudo skali cluster tier

tier plan for host cp-1 (cluster "production")
  database nodes    2 (db-1, db-2)
  deployed tier     synchronous
  available tier    asynchronous

Downgrade the bootstrap database from synchronous to asynchronous
replication. This removes a replica and lowers availability; no data is
deleted.
◆ Apply this database tier change?
│ Yes

  ok  Scale bootstrap database to 2 instance(s) (asynchronous replication)
  ok  Verify replication state

bootstrap database tier: asynchronous
```

The tier apply is deliberately narrow: it scales only the bootstrap
database and restamps the bundle hash, never moving versions or touching
the record; it refuses when the bundle is not current (upgrade converges at
the available tier anyway). Refusing the downgrade would strand the extra
replica unschedulable forever, so it stays an explicit, warned choice.

## 5. Version upgrade (k3s and bundle)

A newer `skali` on a host installed by an older one shows the drift in
its status and moves both versions through the explicit upgrade command:

```console
$ sudo skali cluster status
Skali cluster
  version  2.1.0
  k3s      v1.33.4+k3s1 (pinned)

◆ cp-1
  status     healthy · Skali server (cluster "production")
  k3s        v1.33.3+k3s1 (expected v1.33.4+k3s1)
  bundle     2.0.0 (skali is 2.1.0)
  nodes      7 joined
  bootstrap  database healthy, registry healthy, skalid healthy

$ sudo skali cluster upgrade
Skali cluster
  version  2.1.0
  k3s      v1.33.4+k3s1 (pinned)

upgrade plan for host cp-1 (cluster "production")
  k3s     v1.33.3+k3s1 -> v1.33.4+k3s1
  bundle  2.0.0 -> 2.1.0
  skalid  ghcr.io/hinkolas/skalid:2.1.0

◆ Continue with this upgrade?
│ Yes

  ok  Upgrade k3s to v1.33.4+k3s1 (server)
  ok  Wait for k3s v1.33.4+k3s1 ready
cluster layout
  ...
  ok  Wait for skalid ready

upgrade complete:
  k3s     v1.33.4+k3s1
  bundle  2.1.0

Install logs: /var/lib/skali/logs/init-20260722-104501.log
```

The k3s step re-runs the vendored install script under the new pin; the
node's k3s configuration, the registry mirror, and the pull credential are
never rewritten. The bundle step is the same converge as init: it reuses
the cluster's secrets and stored answers, skips the admin bootstrap, and
prompts only for record fields older versions never gathered (an old
record without a registry domain asks once, with the derivation from the
api domain as the default). Non-interactive runs take `--yes` and fail
with a named field instead of prompting.

Source installations upgrade the same way they initialize, handing the
rebuilt image to the converge: `upgrade --yes --image-tar skalid-dev.tar`
re-converges even when the versions already match, because the new image
id must roll skalid.

Agent nodes carry no bundle, so upgrade moves only k3s there. Upgrade the
servers first, one at a time, waiting for ready between them, then each
agent; a `skali` older than the installed k3s refuses rather than
downgrade. On a multi-node cluster the plan prints the ordered per-host
sequence, and a completed upgrade names what is still drifted:

```console
cluster upgrade order (run per host, one at a time, wait for ready between servers):
  1. cp-1   server  v1.33.3+k3s1 -> v1.33.4+k3s1  (this host)
  2. cp-2   server  v1.33.3+k3s1 -> v1.33.4+k3s1  run there: sudo skali cluster upgrade
  3. db-1   agent   v1.33.3+k3s1 -> v1.33.4+k3s1  run there: sudo skali cluster upgrade
...
next: run sudo skali cluster upgrade on cp-2, then db-1
```

Status marks drifted members with per-node `(needs upgrade)` lines, and an
even server count carries the quorum note.

The bundle is maintained by the node that first initialized the cluster
(the init owner): its canonical record is a bundle-hash input, so a
converge from another server would churn the published record. A secondary
server's status and upgrade plan print `bundle maintained on cp-1` and its
upgrade moves only k3s; init refuses there, naming the owner.

Upgrade is also the migration path for a server installed before the
registry required authentication: such a host has no node pull credential
in its registries.yaml, and install refuses to re-run on a non-fresh
host. The plan names the gap, and the upgrade mints the credential,
rewrites registries.yaml exactly as a fresh install would, and restarts
k3s to load it when no k3s upgrade restarts the service anyway
(containers keep running through the restart). The converge then
publishes the credential in-cluster, so the node keeps pulling once the
registry challenges.

## 6. Repeat execution performs no mutation

```console
$ sudo skali cluster
◆ cp-1
  status     healthy · Skali server (cluster "production")
  k3s        v1.33.3+k3s1 (current)
  bundle     2.0.0 (current)
  nodes      7 joined, 7 expected
  bootstrap  database healthy (synchronous), registry healthy, skalid healthy

nothing to do
◆ What would you like to do?
│ Quit
```

Detection is read-only. No maintenance action runs without being selected.
Repeating a matching `cluster install` or `cluster join` is likewise a
successful no-op.

### Interrupted install recovery

Every new install writes an atomic ownership record in `prepared` state
before k3s configuration is staged. The record advances through
`configured`, `installed`, `starting`, `joined`, and `complete`; its
`.prev` file retains the last valid copy.

Join inputs are validated first. For example, mistyping `.3` as `.4`
produces:

```console
$ sudo skali cluster join --server https://10.1.0.4:6443 \
    --token-file /root/token --capabilities edge
error: join preflight for https://10.1.0.4:6443 failed:
connection was refused; No changes were made.
```

If k3s fails after startup becomes possible, the record, k3s token,
configuration, and datastore are retained:

```console
$ sudo skali cluster
◆ cp-2
  status     interrupted Skali installation (cluster "production")
  install    failed at phase starting
  error      start k3s.service: ... failed to get CA certs ...
  log        /var/lib/skali/logs/install-20260723-220730-a1b2c3d4.log

◆ How should Skali recover this installation?  (use arrow keys, enter to select)
│ ● Resume or edit inputs
│ ○ Diagnose
│ ○ Repair
│ ○ Uninstall
│ ○ Quit
```

Choosing resume accepts a corrected endpoint and retains the installation
ID. `status` and `diagnose` render this state even while k3s is inactive.
`repair` can restore a corrupt primary record from `.prev`, resume the
install transaction, recreate missing k3s service/uninstall files, and
restart the role-correct unit. `uninstall --scope node` works before or
after registration was attempted and removes the transaction record last.

A recordless k3s host is called `orphaned` only when both Skali capability
and cluster labels and the Skali-managed registry mirror are present.
Interactive recovery can explicitly reconstruct ownership, resume with
corrected inputs, or uninstall. Ordinary third-party k3s remains
`unmanaged` and is never adopted or destroyed.

## 7. Diagnosis while Skali is down

The bootstrap database is unavailable; the API and UI are down. `skali
cluster` diagnoses from host state and the Kubernetes API alone:

```console
$ sudo skali cluster diagnose
host cp-1: Skali server (cluster "production")
  ok    k3s service active
  ok    kubernetes api reachable
  ok    nodes 7/7 ready
  fail  bootstrap database: 1/3 instances ready
          pod skali-system/skali-db-2: Pending
            0/3 nodes available: insufficient storage on db-2
  ok    managed registry healthy
  fail  skalid: CrashLoopBackOff (cannot reach its database)

suggested action
  free or expand storage on node db-2, then: skali cluster repair
```

No step above used the Skali API, the product database, or the registry.
Already-running project workloads are unaffected while the control plane is
down. A diagnosis that found problems exits nonzero, so scripts can gate on
it; warnings (a version drift, a pending volume claim) never flip the exit
code.

## 7a. Repair

`skali cluster repair` is diagnose-first: it runs the same read-only
diagnosis, maps the findings onto scoped actions, and confirms each action
individually before touching anything. A clean diagnosis performs no
mutation.

```console
$ sudo skali cluster repair
host cp-1: Skali server (cluster "production")
  fail  k3s service: k3s.service is not active
  ...

suggested action
  skali cluster repair

◆ Restart the k3s service. Workload containers keep running through the restart. Continue?
│ Yes
  ok  Restart k3s
  ok  Wait for k3s v1.33.3+k3s1 ready

host cp-1: Skali server (cluster "production")
  ok    k3s service: active
  ok    kubernetes api: reachable
  ...

$ sudo skali cluster repair
...
nothing to repair
```

The action set: restore an atomic record backup, resume an interrupted host
install (including another authenticated preflight), reinstall missing k3s
service files, restart k3s, rewrite registries.yaml (minting a new node pull
credential, followed by a forced reconverge that publishes it), and
reconverge the bundle from the running cluster's own inputs (unhealthy
components or an interrupted converge whose hash stamp is missing). `--yes`
confirms every planned action for scripts.

Repair never guesses identity: an unreadable installation record is a
refusal pointing at the restore inputs, never a synthesized record. An
agent whose registries.yaml lost its credential is told to re-join with a
fresh token, because a locally minted credential is unknown to the
cluster. On a secondary server the reconverge refuses, naming the init
owner.

## 7b. Restore entry point

```console
$ sudo skali cluster restore
◆ cp-1
  status     fresh

restore rebuilds a Skali installation from three inputs:
  1. the saved installation record (a copy of /var/lib/skali/installation.yaml)
  2. an off-cluster database backup
  3. the registry artifacts (release images and cache contents)

Keep the record with your backups; nothing in the cluster can recreate it.

error: not implemented in this slice: restore arrives with a later milestone
```

The command is the entry point only: it states the recovery contract and
refuses. Record import and database restore land with the backup/restore
milestones.

## 8. Existing Kubernetes cluster

For a cluster whose hosts skali does not administer, the installer runs
from any workstation (macOS included, no VM, no root) with an explicit
kubeconfig. It owns only the Skali system bundle: install and initialize
collapse into one step, and node, k3s, and Kubernetes-version lifecycle
stay with the cluster operator.

```yaml
# existing.yaml
endpoints:
  api: skali.example.com
  registry: registry.example.com
tls:
  issuerEmail: ops@example.com
admin:
  email: nicholas@example.com
  passwordFile: /root/skali-admin-password
skalid:
  image: ghcr.io/hinkolas/skalid:v2.0.0
ingress:
  className: nginx           # required; k3s's traefik cannot be assumed
storage:
  className: fast-ssd        # optional; empty uses the cluster default
database:
  tier: single               # explicit here (no node labels to count)
operators:
  cnpg: install              # or use-existing to reuse the cluster's operator
  certManager: install
```

```console
$ skali cluster install --mode existing-cluster \
    --kubeconfig ~/.kube/config --config existing.yaml
mode: existing cluster (unmanaged hosts)
  This mode installs and maintains only the Skali system bundle. Node
  lifecycle, k3s, and Kubernetes upgrades remain yours.
  ok  Verify cluster version and storage prerequisites
  ok  Apply blessed operators
  ...
  ok  Wait for skalid ready
  ok  Create admin account

Skali is ready:
  https://skali.example.com        api/ui
  https://registry.example.com     managed registry

Application images pull through the registry domain; skalid injects a
pull secret into each project namespace. The registry domain must be
publicly resolvable and issuable for pulls to succeed.
```

Image pulls are the one thing an unmanaged cluster cannot do the managed
way: its nodes have no containerd mirror for `registry.skali.internal`.
So artifact references and pulls both travel the public registry domain,
and `skalid` injects a pull-only credential into every project namespace.

`operators.cnpg: install` refuses when the cluster already runs CNPG (its
CRD is present); set `use-existing` to reuse it, and the converge skips
the vendored operator, relying on the bundle's own health proofs instead.
A default StorageClass is required unless `storage.className` names one,
and the named IngressClass must exist.

The record lives in the cluster (the `skali-installation` ConfigMap), not
on the workstation, so repeat installs, status, upgrade, and uninstall
all read it back. `status`, `upgrade` (reconverge from the record), and
`diagnose` work through the kubeconfig; `init`, `join`, `token`, node-scope
uninstall, `tier`, and host `repair` are refused, each naming what applies
instead. `--mode existing-cluster` requires `--kubeconfig` and refuses
`--vm` and `--image-tar`; the ambient kubeconfig is never consulted.

Caveat: `skalid` holds cluster-wide RBAC over core workload kinds, so
installing into a cluster shared with unrelated tenants is not yet
recommended. Scoping that RBAC is a later slice.

## 9. Scoped uninstall

```console
$ sudo skali cluster uninstall
◆ What should be removed from db-2?
│ This node

Removing node db-2 is refused while skali-system data lives on it:

  skali-system data lives on this node (skali-db-2); relocate it first
```

A server without local system data leaves after stating the quorum
consequence:

```console
$ sudo skali cluster uninstall --scope node --confirm production

Removing this server takes it out of cluster "production":
  - workloads placed on this node lose their local data
  - the remaining 2 servers tolerate 0 failure(s); etcd quorum prefers
    an odd count, so rejoin a server soon
  - k3s itself and /var/lib/skali

  ok  Drain node cp-3
  ok  Remove node from cluster
  ok  Uninstall k3s
  ok  Remove /var/lib/skali
```

The leaving server deletes its own node object first: k3s retires the
etcd member while the cluster is still functional, then the host teardown
follows. The last server refuses to leave while agents remain, and a
removal that would break quorum states the consequence before the typed
confirmation.

Selecting the Skali bundle requires typing the cluster name and lists
what is destroyed: every project namespace, database, bucket, and the
registry contents. Destroying a whole cluster is per-host by design; there is
no single command that reaches into other machines. In existing-cluster mode
the bundle scope removes only the operator namespaces skali installed: an
operator marked `use-existing` at install stays untouched.

## 10. macOS host (Lima VM)

Kubernetes nodes are Linux-only, so on a Mac `skali cluster` manages one
headless Linux VM via Lima and installs the node inside it. The CLI itself
runs rootless on the Mac; privileged work happens inside the VM. The
installation record and all state live in the VM, which is why a stopped VM
is started before any question is answered.

```console
$ skali cluster
Skali cluster
  version  2.0.0
  k3s      v1.33.3+k3s1 (pinned)

◆ minis-01
  status     fresh
  os         macOS (darwin/arm64)
  vm         none (installing creates a Linux VM via Lima)
  k3s        not installed
  record     none

This host is not part of a Skali installation. Install one?

◆ How should the VM connect to the network?
│ bridged
◆ vm cpus
│ 9
◆ vm memory
│ 12GiB
◆ vm disk
│ 100GiB

  ok  Create VM skali (bridged, 9 cpus, 12GiB memory, 100GiB disk)

◆ join token file path (empty to paste the token)
│
◆ join token
│ entered
◆ Which Kubernetes role should this node use?
│ Agent
◆ cluster name
│ production
◆ server url
│ https://192.168.1.10:6443
◆ What should this node run?
│ application, database

  ok  Install k3s v1.33.3+k3s1 (agent)
  ok  Join cluster "production"
  ok  Stamp capability labels on node minis-01
  ok  Write /var/lib/skali/installation.yaml
  ok  Install login LaunchAgent

This node has joined cluster "production". Run skali cluster init on a
server once every planned node has joined.
```

The guest hostname is derived from the Mac's hostname, so a fleet of Macs
yields distinct node names. The bridged network gives the VM its own LAN
address; reserve it in the router's DHCP so the advertised node address
stays stable, and prefer a wired interface, since bridging over WiFi is
unreliable with many access points. The default sizing gives the VM most of
the machine (all cores minus one, memory minus 4GiB); a dedicated fleet Mac
accepts the defaults.

`skali cluster` provisions its own Mac dependencies. Lima is installed from
a pinned, checksum-verified release into a rootless user prefix (an
existing `limactl` on PATH, for example from Homebrew, is detected and used
instead), and a default Lima networks configuration is written when none
exists, with the bridged interface detected from the default route. The
bridged and shared networks additionally run over socket_vmnet, which is
root-owned: everything missing is gathered into one plan that prints the
privileged commands verbatim and runs after a single confirmation.

```console
$ skali cluster install --config node.yaml
Skali cluster
  version  2.0.0
  k3s      v1.33.3+k3s1 (pinned)

this Mac is missing dependencies for the "bridged" network:
  1. install Lima v2.2.0 into ~/.local/share/skali/lima (rootless)
  2. write ~/.lima/_config/networks.yaml (bridged uses interface en0)
  3. install socket_vmnet v1.2.2 root owned:
       sudo tar Cxzf / <verified download> opt/socket_vmnet
  4. allow Lima to launch it:
       limactl sudoers | sudo tee /etc/sudoers.d/lima

◆ Install these dependencies?
│ Yes
  ok  Install Lima v2.2.0
  ok  Write ~/.lima/_config/networks.yaml
Password:
  ok  Install socket_vmnet v1.2.2
  ok  Write /etc/sudoers.d/lima

  ok  Create VM skali (bridged, 9 cpus, 12GiB memory, 100GiB disk)
  ...
```

Each provisioning step is skipped when its result already exists, so an
interrupted run resumes with only the missing pieces in the plan.
Non-interactive runs fail naming the missing components and `--yes`. A
networks.yaml that exists but lacks the requested network is never edited;
the run fails with instructions instead, because that file is operator
configuration. The user-v2 network needs no root components at all.

Non-interactive installs describe the VM in the node configuration; every
field is optional:

```yaml
# node.yaml (macOS)
role: server
capabilities: [application, database, object-storage, registry, edge]
vm:
  network: bridged
  cpus: 9
  memory: 12GiB
  disk: 100GiB
```

The login LaunchAgent starts the VM at login; the installer warns when
macOS auto login is disabled, because a headless Mac then stays down after
a reboot until someone logs in. Repeat runs decorate the status with the
VM:

```console
$ skali cluster status
Skali cluster
  version  2.0.0
  k3s      v1.33.3+k3s1 (pinned)

◆ minis-01
  status     Skali agent (cluster "production")
  vm         skali (Lima, network bridged)
  k3s        v1.33.3+k3s1 (current)
  ...
```

Node-scope uninstall removes the whole VM: k3s, the record, and all state
go with it, and the LaunchAgent is unloaded.

```console
$ skali cluster uninstall --scope node --confirm production
...
  ok  Stop VM skali
  ok  Delete VM skali
  ok  Remove login LaunchAgent

This Mac is fresh again.
```

Linux hosts are unaffected by all of this: they stay native, and a `vm`
block in their node configuration is refused.
