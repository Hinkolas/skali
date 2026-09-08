# First prerelease safety changes

This release establishes a fresh-install baseline. Environment namespaces use
`skali-<environment UUID>` and workload resource names include an identity hash.
Existing names, volumes, and namespaces are never automatically renamed,
adopted, or deleted. Migration refuses databases containing legacy environments;
startup also refuses legacy managed namespaces before starting controllers.

Before replacing an existing development installation, stop writes and export
its databases, buckets, and volume contents using the old installation. Keep
those exports outside that cluster and verify they can be read. Retain the
manifests and the values needed to redeploy separately. Create a separate clean
installation, deploy the manifests, and restore application data into its new
resources. Verify applications and data before explicitly retiring the old
installation. This is a manual data transfer, not an in-place upgrade or a promise
that an old installation database can be restored into this release.

Each public hostname belongs to one environment. Applications in that environment
may share paths; other environments cannot claim any path on its hostname.
Platform, registry, and S3 hosts are reserved through `SKALI_RESERVED_HOSTS`
(semicolon-separated; the managed bundle supplies these automatically).
Configuration cannot reserve a hostname already claimed by an environment.
Reservations persist conservatively after configuration changes.

Removing a route does not immediately transfer its hostname. Claims are retained
until reconciliation confirms that the old routers are absent. Failed removal
retains ownership and retries. Rollback and automatic recovery must reclaim
released hosts and can fail if another environment has acquired them. Conflicts
leave the existing target unchanged. Raw Kubernetes changes are operator actions;
the claim system governs routes managed through skali.

Backup policies are accepted but inactive: skali does not run scheduled backups
or enforce retention. Create backups manually. Deploy, promote, redeploy, and
rollback report this limitation when a revision declares policies. Use
`skali backup create`, `skali backup list`, and `skali backup --help` for manual
backup, restore, and removal operations. A successful deploy is not evidence of
automatic data protection.

Volume restore is destructive and may leave partial restored contents on failure.
It preserves numeric ownership, permissions, and timestamps on Linux, including
the volume root in new archives. Unsupported entries and metadata failures fail
the operation explicitly. Archives support regular files, directories, and
symlinks; symlink contents are never followed during extraction.

Release verification: `scripts/check-release-snapshot.sh` builds a nonpublishing
snapshot and checks its metadata against the binaries and installer pin. CI also
verifies metadata downloaded from the exact tag after a real publication.
