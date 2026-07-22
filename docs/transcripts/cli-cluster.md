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

## 1. Interactive fresh single node

```console
$ sudo skali cluster
skali 2.0.0 (k3s v1.33.3+k3s1 pinned)

host cp-1: fresh
  os      Ubuntu 24.04 (linux/amd64)
  k3s     not installed
  record  none

This host is not part of a Skali installation. Install one?

  role                  [1] server  [2] agent          : 1
  first server          creates cluster "production"   : yes
  capabilities          application, database, object-storage,
                        registry, edge                 : all
  api/ui domain                                        : skali.example.com
  registry domain                                      : registry.example.com
  tls issuer email                                     : ops@example.com

  ok  Install k3s v1.33.3+k3s1 (server)
  ok  Stamp capability labels on node cp-1
  ok  Write /var/lib/skali/installation.yaml

This is the only node so far. Initialize Skali now? [Y/n] y

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

  admin email     : nicholas@example.com
  admin password  : (prompted, not echoed)
  ok  Create admin account

Skali is ready:
  https://skali.example.com        api/ui
  https://registry.example.com     managed registry

Install logs: /var/lib/skali/logs/init-01J9X2.log
```

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
role: agent
capabilities: [database]
join:
  server: https://cp-1.internal:6443
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
  sudo skali cluster join --server https://cp-1.internal:6443 \
    --token-file <file> --role agent --capabilities <list>

join token (write it to <file> on the joining host, mode 0600):
  skali1.eyJrM3MiOiJLMTAuLi4iLCJwdWxsIjoiLi4uIn0
```

The printed token is composite: it bundles the k3s join token with the
cluster's registry pull credential, so one paste enrolls the node for both.
A raw k3s token still joins, but the node then pulls from the managed
registry unauthenticated and fails once token auth challenges it.

On each additional host (joining is initiated per host; the installer never
stores SSH credentials or reaches into other machines):

```console
$ sudo skali cluster join --server https://cp-1.internal:6443 \
    --token-file /root/token --role agent --capabilities database
  ok  Install k3s v1.33.3+k3s1 (agent)
  ok  Join cluster "production"
  ok  Stamp capability labels on node db-1
  ok  Write /var/lib/skali/installation.yaml
```

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
host cp-1: healthy Skali server (cluster "production")

  database nodes    3 (db-1, db-2, db-3)
  deployed tier     asynchronous
  available tier    synchronous

  [1] status        show installation health
  [2] apply tier    upgrade system databases to synchronous
  [3] upgrade       k3s / bundle versions
  [4] repair        diagnose and repair
  [5] uninstall     scoped removal
  : 2

Upgrade the bootstrap database from asynchronous to synchronous quorum
replication. This adds a replica and briefly reconfigures replication; no
data is deleted. Continue? [y/N] y

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

## 5. Version upgrade (k3s and bundle)

A newer `skali` on a host installed by an older one shows the drift in
its status and moves both versions through the explicit upgrade command:

```console
$ sudo skali cluster status
skali 2.1.0 (k3s v1.33.4+k3s1 pinned)

host cp-1: healthy Skali server (cluster "production")
  k3s        v1.33.3+k3s1 (expected v1.33.4+k3s1)
  bundle     2.0.0 (skali is 2.1.0)
  nodes      7 joined
  bootstrap  database healthy, registry healthy, skalid healthy

$ sudo skali cluster upgrade
skali 2.1.0 (k3s v1.33.4+k3s1 pinned)

upgrade plan for host cp-1 (cluster "production")
  k3s     v1.33.3+k3s1 -> v1.33.4+k3s1
  bundle  2.0.0 -> 2.1.0
  skalid  ghcr.io/hinkolas/skalid:2.1.0

Continue? [y/N] y

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
server first, then run upgrade on each node; a `skali` older than the
installed k3s refuses rather than downgrade.

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
host cp-1: healthy Skali server (cluster "production")
  k3s        v1.33.3+k3s1 (current)
  bundle     2.0.0 (current)
  nodes      7 joined, 7 expected
  bootstrap  database healthy (synchronous), registry healthy, skalid healthy

nothing to do
  [1] status  [2] apply tier  [3] upgrade  [4] repair  [5] uninstall  [q] quit
  : q
```

Detection is read-only. No maintenance action runs without being selected.

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
down.

## 8. Existing Kubernetes cluster

```console
$ skali cluster install --mode existing-cluster \
    --kubeconfig ~/.kube/config --config init.yaml
mode: existing cluster (unmanaged hosts)
  This mode installs and maintains only the Skali system bundle. Node
  lifecycle, k3s, and Kubernetes upgrades remain yours.
  ok  Verify cluster version and storage prerequisites
  ok  Apply blessed operators
  ...
```

In this mode `join`, node removal, and Kubernetes upgrades are refused.

## 9. Scoped uninstall

```console
$ sudo skali cluster uninstall
scope of removal on host db-2 (cluster "production"):

  [1] this node       drain and remove db-2 from the cluster; project data
                      placed only on this node is relocated first or the
                      removal is refused
  [2] skali bundle    remove Skali and all project workloads and data from
                      the cluster; keep bare k3s running
  [3] entire cluster  not available from one host: destroying a cluster is
                      per-host, run uninstall on every member
  : 1

Removing node db-2 relocates 2 database instances. The database tier drops
from synchronous to asynchronous. Type the node name to continue: db-2
  ok  Drain and relocate database instances
  ok  Remove node from cluster
  ok  Uninstall k3s and remove /var/lib/skali
```

Removing the Skali bundle (`[2]`) requires typing the cluster name and lists
what is destroyed: every project namespace, database, bucket, and the
registry contents. Destroying a whole cluster is per-host by design; there is
no single command that reaches into other machines.

## 10. macOS host (Lima VM)

Kubernetes nodes are Linux-only, so on a Mac `skali cluster` manages one
headless Linux VM via Lima and installs the node inside it. The CLI itself
runs rootless on the Mac; privileged work happens inside the VM. The
installation record and all state live in the VM, which is why a stopped VM
is started before any question is answered.

```console
$ skali cluster
skali 2.0.0 (k3s v1.33.3+k3s1 pinned)

host minis-01: fresh
  os      macOS (darwin/arm64)
  vm      none (installing creates a Linux VM via Lima)
  k3s     not installed
  record  none

This host is not part of a Skali installation. Install one?

  vm network (bridged, shared, user-v2) [bridged]:
  vm cpus [9]:
  vm memory [12GiB]:
  vm disk [100GiB]:

  ok  Create VM skali (bridged, 9 cpus, 12GiB memory, 100GiB disk)

  role                  [1] server  [2] agent          : 2
  cluster name          [production]                   :
  capabilities          application, database          : application, database
  server url            https://cp-1.internal:6443     : https://192.168.1.10:6443
  join token file path  (empty to paste the token)     :
  join token            (pasted, not echoed)           :

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
skali 2.0.0 (k3s v1.33.3+k3s1 pinned)

this Mac is missing dependencies for the "bridged" network:
  1. install Lima v2.2.0 into ~/.local/share/skali/lima (rootless)
  2. write ~/.lima/_config/networks.yaml (bridged uses interface en0)
  3. install socket_vmnet v1.2.2 root owned:
       sudo tar Cxzf / <verified download> opt/socket_vmnet
  4. allow Lima to launch it:
       limactl sudoers | sudo tee /etc/sudoers.d/lima

Proceed? [y/N] y
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
skali 2.0.0 (k3s v1.33.3+k3s1 pinned)

host minis-01: Skali agent (cluster "production")
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
