// The "org" line of the shell (sidebar header, breadcrumb root, auth
// footer), synthesized from instance meta plus list lengths. skali has no
// org entity; this is a view-model, not an API shape.

import type { SystemMeta } from '$lib/types/system';

export interface OrgView {
	name: string;
	version: string;
	/** A newer release the daemon knows about; drives the System badge. */
	update_available: string | null;
	project_count: number;
	node_count: number;
}

export function buildOrg(
	meta: SystemMeta | null,
	projectCount: number,
	nodeCount: number
): OrgView {
	return {
		name: meta?.name || 'skali',
		version: meta?.version ?? '',
		update_available: meta?.update_available?.version ?? null,
		project_count: projectCount,
		node_count: nodeCount
	};
}
