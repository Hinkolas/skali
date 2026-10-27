# Application storage

Skali provisions a persistent volume for every `volumes` entry an
application declares. Managed Linux clusters and Docker-based local
development use the k3s `local-path` provisioner.

Application volumes are local to one node. Declared sizes are advisory,
per-volume usage is not measurable, and a lost node disk means lost volume
data. `skali plan` and `skali deploy` warn when a manifest declares volumes.
Use environment backups to protect their contents. Volumes removed from a
manifest are retained; see [limitations](limitations.md).

```yaml
applications:
  files:
    image: example.invalid/files:1
    volumes:
      data:
        mountPath: /data
        size: 10GB
```

## Where data lives

| Data | Backing | Behavior |
| --- | --- | --- |
| Application volumes | `local-path` | A volume pins its workload to one node; no replication or size enforcement. |
| Managed registry | `local-path` | The registry keeps its node pin. |
| Managed databases (CNPG) | `local-path` | PostgreSQL replicates at its own layer according to the database's availability tier. |
| Object storage (SeaweedFS) | Local hostPath disks | SeaweedFS replicates object bytes at its own layer; bucket directories live in the shared PostgreSQL pool. |

Each application's temporary storage (its writable container layer, logs,
and emptyDirs) is measured from the kubelet and shown in the Studio.
`resources.limits.temporaryStorage` caps it: the kubelet evicts a pod that
exceeds the limit. This is separate from persistent volume capacity.

## Resizing

Skali rejects a reduction in a declared volume size before deployment.
The local-path provisioner also does not support expanding existing claims;
increasing their requested size is rejected by Kubernetes during apply.
The host disk's free space limits what can actually be stored.

## Removed storage backend

Longhorn is no longer supported. The storage-driver prompt, CLI flag,
registry storage migration command, and automatic iSCSI prerequisite
installation have been removed. Older init files declaring `storage.driver:
local` still work; new configurations can omit the block. An installation
record selecting another driver is refused before initialization or bundle
reconvergence; there is no automatic conversion of existing volumes.

Packages and host configuration installed by an earlier release are not
removed during an upgrade, since other host software may use them.
