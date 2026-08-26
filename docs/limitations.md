# Known limitations

Things skali does not do yet, stated so nobody finds out the hard way.
Each one is deliberate for now and tracked in [`ROADMAP.md`](../ROADMAP.md).

## Volumes are never reclaimed automatically

The reconciler prunes stateless objects (workloads, services, routes) that
disappear from the manifest, but never PersistentVolumeClaims and never
namespaces. Consequences:

- Removing a `volumes:` entry from an application, or removing the whole
  application, leaves its volume and data in the environment's namespace.
  Nothing uses it, nothing reports it, and its disk stays allocated until
  you delete the claim by hand:

  ```sh
  kubectl -n skali-<project>-<environment> get pvc
  kubectl -n skali-<project>-<environment> delete pvc <name>
  ```

- Renaming a volume key is a removal plus a creation: the new volume
  starts empty and the old one stays behind as above.
- `skali env rm` (a purge) is the one path that removes everything: the
  namespace goes with every volume in it.

Declared sizes are only enforced on the `longhorn` driver; on `local` the
size is advisory and volumes pin their pods to one node. See
[`storage.md`](storage.md).

## Deleting a project does not tear its environments down

`DELETE /v1/projects/{id}` (the console's delete-project action) removes
the project and its environment records, but does not purge the
environments' namespaces: their workloads, databases, buckets, and volumes
keep running on the cluster, and skalid only logs
`orphaned managed namespace retained`. Purge every environment first with
`skali env rm <name>`, then delete the project. Namespaces already orphaned
this way can only be removed by hand (`kubectl delete namespace`).

## The registry node cannot be removed

`skali cluster node remove` refuses the node that holds the managed
registry's data ("registry migration is not implemented"). Plan the seed
node as permanent, or, on the `longhorn` driver, run
`skali cluster storage-migrate` first, which moves the registry volume onto
replicated storage; the removal guard is still in place afterwards and will
be relaxed once migration between nodes is implemented.

## Disaster recovery is manual

There is no `skali cluster restore` yet. Recovering an installation on a
fresh host means reinstalling, restoring the system database from your own
backup, and re-pushing images with `skali deploy`. Keep a copy of
`/var/lib/skali/installation.yaml` with your backups: nothing in the cluster
can recreate it. Environment data (databases, buckets, volumes) is covered
by `skali backup`, but skali's own state is not backed up automatically.

## Builds run on your machine

Every `skali deploy` builds and pushes from the machine running the CLI;
there is no push-to-deploy or in-cluster builder. `--build auto` resolves
to `local` today.

## Single management plane

`skalid` runs as one deployment. The applications it manages keep running
if it is down, but deploys, the console, and the API are unavailable until
it is back.
