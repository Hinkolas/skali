# Cluster enrollment and recovery

Enrollment registers a host and starts its outbound node agent. It does not
install k3s. Review `skali cluster plan` on a controller, then apply the topology
or run the first `skali cluster init` when all intended hosts have enrolled.

## Joining a host

On a controller, issue one invitation per new host:

```sh
sudo skali cluster token --role agent --capabilities database
```

Copy the generated `sudo skali cluster join --token 'skali.…'` command to the
joining host. Newly issued tokens include coordinator discovery endpoints.
Preflight verifies the invitation and supplies its authoritative role, cluster,
and allowed capabilities. An explicit capability set becomes the join default;
an unrestricted invitation asks which capabilities to use. Override the defaults
with `--capabilities application,edge` when the invitation permits that subset.

The interactive flow proposes the joining host's source address for traffic to
the coordinator and asks about public addresses. For unattended joins, declare
addresses explicitly when needed:

```sh
sudo skali cluster join --token 'skali.…' \
  --node-ip 10.10.1.10 --public-ip 203.0.113.10
```

Existing tokens without endpoints remain valid. Supply the coordinator as an
argument or with `--server`; an interactive join asks for it if missing.
An explicit coordinator overrides discovery hints. Automatic failover only
connects to coordinators authenticated by the token's CA pin.

Alternative token inputs:

```sh
sudo skali cluster join --token-file /root/join.token
sudo skali cluster join --token-file - < /root/join.token
sudo env SKALI_JOIN_TOKEN='skali.…' skali cluster join
```

`--token` and `--token-file` are mutually exclusive. Either explicit input takes
precedence over `SKALI_JOIN_TOKEN`. File and stdin tokens may contain line wraps,
spaces, or CRLF line endings. The interactive token field displays editable text:
Enter adds a line, Ctrl+D submits, and Ctrl+C cancels. In a plain terminal, submit
with a blank line after the token. Password fields elsewhere remain masked.

## Interrupted enrollment

Skali checks local prerequisites before redeeming an invitation, and saves the
node identity, settings, pending key/CSR, and temporary invitation locally before
redemption. Sensitive files are root-only. Temporary credentials are removed after
the agent's durable credentials have been written.

Temporary transport failures and HTTP 408/429/500/502/503/504 responses retry with
backoff. Each request is limited to 30 seconds and the combined preflight and
enrollment request budget is two minutes. Ctrl+C interrupts the wait. Invalid
credentials, wrong trust, disallowed capabilities, and identity conflicts fail
immediately.

If the coordinator saved a node but its response was lost, a retry uses the same
identity and CSR and returns credentials without adding another node or revision.
After interruption, use:

```sh
sudo skali cluster join
```

The interactive `sudo skali cluster` menu also offers enrollment recovery. If the
invitation expired or was revoked before credentials were saved, create a new
invitation with the same role and capabilities and supply it with `--token`.
Keep the existing local record and pending CSR. If only local agent startup
failed, saved credentials allow recovery without contacting the invitation API.

A preflight failure before a local record is created requires rerunning the
original join command. If a disk failure prevented saving the token itself, supply
that token again; Skali keeps any successfully saved node identity.

Status distinguishes an incomplete enrollment, waiting for the first heartbeat,
agent connected and awaiting apply, and a joined node. A candidate without k3s
is not reported as a Kubernetes API failure.

## Cancelling an unapplied node

On a controller:

```sh
sudo skali cluster node remove skali-db-01
sudo skali cluster plan
```

If the node was never part of an applied or currently applying target, removal
cancels the enrollment immediately. Its addition disappears from the plan and its
name becomes available, even if it never sent a heartbeat. No forced forget is
needed. A retained cancellation identity rejects delayed enrollments; an old
agent can only receive cleanup. After cancellation, joining that same physical
host requires clearing its local enrollment with `sudo skali cluster uninstall`
and using a new invitation. Cleanup of an unapplied enrollment retains the installer binary,
so a new join does not require reinstalling the CLI package. A cancelled identity
cannot be resumed.

An installed or already-targeted node still follows the normal drain and removal
plan. Registry data protection and quorum safeguards continue to apply.
Normal status hides cancelled and removed history; inspect it with:

```sh
sudo skali cluster status --all
```

## Upgrading an existing alpha.2 cluster

This code must be released and installed before the new commands are available.
No cluster reset, record deletion, storage migration, or Kubernetes reinstall is
required. Preserve all installation records and pending enrollment key/CSR files.

1. Install the new release's CLI **and** its `skali-hostd_linux_<arch>` asset
   as `/usr/local/libexec/skali-hostd` on every active controller, one
   controller at a time (`install.sh` installs the CLI only). Restart
   `skali-coordinator.service` and `skali-node-agent.service` after replacing
   its binaries. Verify its status
   before proceeding. Upgrade all active coordinators before using cancellation
   or capability discovery; an old coordinator does not enforce the new
   cancellation state. Pending server candidates are not active coordinators yet.
2. Install that same release on pending hosts and restart existing node agents.
   If an agent was stopped during diagnosis, start it again. For a host whose
   enrollment never completed, run the join command instead of trying to start
   a service with missing credentials.
3. On `skali-db-01`, resume with `sudo skali cluster join`. Alpha.2 did not save the
   invitation token, so it may ask for one; noninteractive recovery can use
   `sudo skali cluster join --token 'skali.…'`. Reuse the last invitation if it
   remains valid, or issue a replacement. Saved addresses, identity, and
   capabilities are reused.
4. On `skali-ctrl-01`, run `sudo skali cluster status` and confirm that db-01 has a
   recent heartbeat. Enroll the remaining hosts, review `sudo skali cluster plan`,
   and run the first `sudo skali cluster init` when the intended topology is ready.

Coordinator request durations and mirror failures appear in
`journalctl -u skali-coordinator.service`. Diagnostic ConfigMap mirrors are now
reconciled asynchronously by the leader. The singleton remains authoritative;
mirror failure cannot turn a committed enrollment into a rejection. Mirror state
is refreshed at startup and every minute; unchanged revisions are not rewritten.
The coordinator's Kubernetes client uses 50 QPS / 100 burst: eleven agents alone
require about 8.8 QPS for their regular heartbeat requests.
