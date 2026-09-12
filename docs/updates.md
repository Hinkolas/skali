# Cluster updates

A managed cluster has one Skali release. **System → Software update** and
`skali cluster upgrade` submit the same durable operation: update host services
on controllers serially, then agents serially, reconcile the shared platform,
and verify every component. You do not SSH into each node to update it.

`skalid` runs inside Kubernetes and serves the API and console. Each node also
runs `skali-agent.service` from the `skali-hostd` binary. Controllers run
`skali-coordinator.service` from that binary too. Those host services manage
installation, enrollment, and updates even when the platform is restarting.
Kubernetes changes only when the selected release changes its pinned version;
an ordinary Skali update does not unnecessarily restart Kubernetes.

## Normal operation

On a managed installation, select and authenticate the intended remote with
`skali remote add` or your existing remote configuration, then run:

```sh
skali cluster upgrade
skali cluster upgrade --wait
skali cluster upgrade --version v0.1.0-alpha.5 --yes --wait
```

The exact version above is an example; it must be a published release with
assets for your nodes. Without `--version`, Skali scans for the newest release
on the cluster's configured stable or beta channel. Stable excludes
prereleases; beta includes them. The CLI shows the remote, target, and whole
cluster scope before confirmation. `--yes` accepts that confirmation; it does
not bypass administrator authorization, reauthentication, cluster health,
deployment, or backup checks. Run with the same user's authenticated CLI
configuration; using `sudo` may select root's separate configuration.

`--wait` follows progress. Closing the terminal or losing the connection does
not cancel an accepted operation. Repeating the command attaches to a running
update or offers to retry a failed one. A different target is refused while
an operation is running or waiting for retry.

The console shows **Preparing**, **Updating nodes**, **Updating platform**, and
**Verifying**. **Update details** contains node identities, host agent and
coordinator versions, Kubernetes versions, heartbeats, and step errors.

The displayed **Skali version** remains the last converged release until fresh
host and coordinator reports, Kubernetes readiness and versions, and the
completed `skalid` rollout all match the target exactly. Different versions
during a rolling update are expected. An interrupted update is never considered
complete merely because the console has already moved to the target.

## Finish or retry an update

If the platform is alpha.4 while the recorded cluster or host services are
alpha.3, Skali offers **Finish update** to alpha.4. This does not depend on the
release feed finding something newer or the channel including prereleases.
The highest known installed release is the proposed finish target. A host
behind an otherwise converged cluster also offers same-release repair.

Newly detected divergence needs an explicit **Finish update**, even with
automatic updates enabled. Automatic updates still install newly available
releases on the selected channel. Once accepted, either kind of update
continues through service restarts without another confirmation.

A failed operation keeps completed work. Correct the error in **Update
details**, then choose **Retry**, or run `skali cluster upgrade` again. Restore
disconnected nodes before retrying. A newer installed component is never
silently downgraded. Unknown or development versions require investigation;
they are not treated as current. Cancelled and removed enrollment records may
remain in history, including reused hostnames; updates use immutable node IDs
and exclude those retired records. Deleting enrollment history is unnecessary.

## Explicit controller recovery

When the API is unavailable, a managed controller can submit the operation
using its local cluster authority:

```sh
sudo skali cluster upgrade --recover --version v0.1.0-alpha.5 --wait
```

This path requires both `--recover` and an exact published `--version`. It
checks the live platform image, node reports, cluster state, release assets,
architecture support, and version protections. It cannot check deployment or
backup activity in the product database. The CLI states this before confirming;
ensure those operations have stopped before accepting recovery. `--yes` is
available for an operator who has already made those checks.

Recovery never activates after an API or authentication error automatically.
It runs only on a managed controller with local Kubernetes authority. It uses
the same journal and coordinator, so disconnecting still does not cancel it.
It can resume a failed operation only at that operation's target.

### Moving from alpha.4 host services

Use a CLI built from the release containing this coordinated updater. Older
daemons do not expose the new aggregate status or report coordinator versions.
If the normal command reports that the daemon lacks coordinated CLI support,
use the explicit controller recovery command with the **new release's version**
after checking activity. Do not target alpha.4 to install the new updater.
Alternatively, use the previous CLI's platform maintenance path once to
bootstrap the new platform, then select the new release explicitly.

The new journal is stored alongside the old state document in the existing
cluster ConfigMap. Alpha.4 host writers preserve that entry during the rolling
upgrade. Completion requires controllers running the new reporting code; an
old host agent's version alone cannot prove its coordinator restarted.

## Updating the CLI

`skali upgrade` updates the `skali` binary on the machine it runs on and
nothing else; a cluster moves with `skali cluster upgrade`. It follows the same
channels as `install.sh`: stable excludes prereleases, beta includes them. The
default is stable, or beta when the installed CLI is itself a prerelease, so an
alpha install keeps following alphas without a flag.

```sh
skali upgrade
skali upgrade --channel stable
skali upgrade --version v0.1.0-alpha.5
```

`--version` names an exact published release and overrides the channel; it is
the way to move back to an earlier release. Every download is verified against
the release's `checksums.txt` before the binary is replaced, and the new binary
must report the target version or the previous one is restored. Shell
completion scripts that `install.sh` or `skali completion install` put in place
are regenerated from the new binary as the last step.

Neither `install.sh` nor `skali upgrade` installs `skali-hostd`. The host
daemon is fetched by `skali cluster install`, `join`, and `repair` on the node
that needs it, from the release the CLI is, verified the same way and cached
under `~/.cache/skali/hostd`. A development build has no release to fetch from
and uses the `skali-hostd` next to its own binary (`task build` puts one in
`bin/`) or `--hostd-bin`. The running `skali-hostd` on a cluster node is only
ever replaced by the cluster update.

On Linux the CLI lives in `/usr/local/bin`, so updating it needs
`sudo skali upgrade`; the command says so before downloading anything. A
development build (anything that is not a tagged release) is refused unless
`--version` says which release should replace it.

## Maintenance paths

Legacy installations without the managed coordinator retain their per-host
`skali cluster upgrade` behavior, using the CLI's release. Explicit
`--image-tar` source-image maintenance also retains its existing host behavior.
These paths sit outside coordinated release updates. They can create version
differences that need a subsequent managed update; they are not the normal way
to install a release across a managed cluster. `--version` and `--wait` apply
to coordinated release updates, and `--recover` cannot use `--image-tar`.

Publishing a release and applying it to a live cluster are separate operator
actions. Building or testing this code performs neither.
