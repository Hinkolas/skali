// Mirror catalog payloads (GET /v1/registry/images).

/** One imported image: an upstream tag pinned to a digest in the cluster registry. */
export interface RegistryImage {
	id: string;
	/** Mirror-relative, e.g. "mirror/docker.io/library/postgres". */
	repository: string;
	tag: string;
	/** The pinned upstream manifest/index digest ("sha256:…"). */
	digest: string;
	/** Unique blob bytes across the whole (multi-arch) index; 0 = unknown. */
	size_bytes: number;
	imported_at: string;
	/** Advances when a re-import moves the pin. */
	updated_at: string;
}
