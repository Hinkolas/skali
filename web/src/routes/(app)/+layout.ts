// Shell data for the whole authenticated area: the caller's project list
// (no summaries: those belong to the pages that show them), instance meta,
// and, for instance admins only, the observed nodes (/v1/nodes answers 403
// to everyone else). Runs once per hard load and again on invalidateAll();
// tab navigation does not re-run it.
//
// Everything here must stay cheap: no page paints before this resolves.
// The three requests are batched list reads on the daemon; /v1/system/meta
// still costs it a database read plus a cluster ConfigMap read per call.

import { error } from '@sveltejs/kit';
import { isInstanceAdmin } from '$lib/access';
import { apiFetch } from '$lib/api/client';
import { requireUser } from '$lib/session';
import { buildOrg } from '$lib/models/org';
import type { ClusterNode, NodesResponse } from '$lib/types/nodes';
import type { Project } from '$lib/types/project';
import type { SystemMeta } from '$lib/types/system';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ fetch, url }) => {
	const user = await requireUser(fetch, url);
	const admin = isInstanceAdmin(user);

	const [projectsRes, metaRes, nodesRes] = await Promise.all([
		apiFetch(fetch, '/v1/projects'),
		apiFetch(fetch, '/v1/system/meta'),
		admin ? apiFetch(fetch, '/v1/nodes') : Promise.resolve(null)
	]);
	if (!projectsRes.ok) error(502, 'Could not load projects');
	const { projects } = (await projectsRes.json()) as { projects: Project[] };

	// Meta and nodes degrade gracefully: a partial shell beats an error page.
	const meta = metaRes.ok ? ((await metaRes.json()) as SystemMeta) : null;
	// null: not asked (not an instance admin). []: asked, cluster not
	// observed. The sidebar footer tells the two apart.
	const nodesBody = nodesRes?.ok ? ((await nodesRes.json()) as NodesResponse) : null;
	const nodes: ClusterNode[] | null = admin ? (nodesBody?.nodes ?? []) : null;

	return {
		user: user,
		org: buildOrg(meta, url.hostname, projects.length, nodes?.length ?? null),
		projects,
		nodes,
		nodesObservation: nodesBody?.observation ?? null
	};
};
