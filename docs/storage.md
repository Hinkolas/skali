# Application storage

Skali provisions a persistent volume for every `volumes` entry an
application declares. Where those volumes live is a cluster-level choice
made at initialization, the storage driver:

- `local` (the default) keeps volumes on the k3s `local-path`
  provisioner. Nothing extra runs on the cluster, which suits
  installations whose state lives mostly in buckets and databases; the
  trade-off is that declared sizes are not enforced, per-volume usage is
  not measurable, volumes pin their pods to one node, and a lost node
  disk means lost volume data.
- `longhorn` deploys Longhorn, a replicated block-storage layer the
  installer owns: declared sizes are enforced, usage is measurable, and a
  volume survives the loss of the node it was written on. The cost is
  Longhorn's operator footprint on every node.

Pick it at init (`storage.driver` in init.yaml, the interactive prompt,
or `skali cluster init --storage-driver longhorn`). A cluster on `local`
can switch to `longhorn` at any time by re-running init with the flag;
the reverse switch is not supported. The driver enum is also the seam for
future provider-native drivers (for example hcloud-csi).

```yaml
applications:
  files:
    image: example.invalid/files:1
    volumes:
      data:
        mountPath: /data
        size: 10GB
```

## Where data lives (longhorn driver)

| Data | Backing | Why |
| --- | --- | --- |
| Application volumes | Longhorn class `skali-app` | enforced size, CSI usage stats, replication |
| Managed registry | Longhorn class `skali-app` | frees the registry from its node pin |
| Managed databases (CNPG) | local disk (`local-path`) | postgres replicates at its own layer |
| Object storage (SeaweedFS) | local disk (hostPath) | seaweed replicates at its own layer |

Under the `local` driver every row reads `local-path` (the registry keeps
its node pin), and the rest of this section does not apply.

The `skali-app` StorageClass is rendered by the bundle and is never the
cluster default; `local-path` keeps that role. Replicas per volume follow
the topology at initialization: one per application-capable node, capped
at three. Replica data only lands on nodes labeled
`node.longhorn.io/create-default-disk=true`, which the installer stamps on
application-capable nodes at registration and re-stamps on every converge.

Local development (`skali dev`) keeps every volume on the k3d default
`local-path` class: dev data is throwaway and Longhorn does not run in
k3d. The rendering is identical apart from the storage class name.

## Host prerequisites

Longhorn volumes attach over iSCSI. `skali cluster install` and
`skali cluster upgrade` install `open-iscsi`, enable `iscsid`, and, where
`multipathd` runs, write a blacklist at
`/etc/multipath/conf.d/skali-longhorn.conf` so multipath never claims a
Longhorn device. This happens under both drivers: the footprint is one
small package and an idle daemon, and it keeps every host permanently
ready so enabling longhorn later needs no per-host pass.
`skali cluster diagnose` checks all of this and `skali cluster repair`
fixes it.

## Resizing

Volumes only grow. Raising `size` in the manifest expands the volume
online on the next deploy; a deploy that shrinks a volume is refused up
front with the volume named. On `local-path` volumes (dev clusters and
the `local` driver) sizes cannot change at all; growth there is rejected
by the cluster at apply time.

## Enabling longhorn on an existing cluster

A cluster on the `local` driver (including any installation that predates
the driver choice) keeps working untouched: the converge keeps rendering
the legacy local-path shapes until each piece is migrated explicitly,
because a claim's storage class is immutable.

Order of operations:

1. `skali cluster upgrade` on every node, so every host carries the
   Longhorn prerequisites, then `skali cluster init --storage-driver
   longhorn` on the server that maintains the bundle. The converge
   installs the Longhorn operators and renders the `skali-app` class.
   From this point, newly created volumes (new apps, new environments)
   land on `skali-app`; existing claims stay where they are.
2. `skali cluster storage-migrate` on the server that maintains the
   bundle. This recreates the registry volume on `skali-app` and drops
   the registry's node pin. Registry contents are discarded, not copied:
   images are re-pushed by the next `skali deploy` of each project, and
   running workloads keep their current images throughout.
3. Per project, move the app volumes (skip projects without volumes).
   With the project checked out and bound to the remote:

   ```sh
   # 1. Snapshot everything, volumes included, to the external S3 target.
   skali backup create --environment production

   # 2. Delete the volume-backed workloads and their claims together. The
   #    level-triggered reconciler recreates both immediately; the new
   #    claims render on skali-app and the pods start on empty volumes.
   kubectl -n skali-<project>-production delete deploy,pvc -l skali.dev/managed=true

   # 3. Replay the volume (and database/bucket) contents; the restore
   #    stops the workloads itself while it writes.
   skali backup restore <backup-id>
   ```

   Verify with `kubectl get pv`: the project's volumes now name
   `driver.longhorn.io` as their provisioner.

Interrupting `storage-migrate` is safe: every step is level-triggered and
rerunning the command finishes the job. The per-project sequence is safe
to repeat from the backup restore onward.
