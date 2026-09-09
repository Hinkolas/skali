// Shell data for the whole authenticated area: projects (with summaries),
// observed nodes, and instance meta. Runs once per hard load and again on
// invalidateAll(); tab navigation does not re-run it.

import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import { requireUser } from '$lib/session';
import { buildOrg } from '$lib/models/org';
import type { NodesResponse } from '$lib/types/nodes';
import type { Project } from '$lib/types/project';
import type { SystemMeta } from '$lib/types/system';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ fetch, url }) => {
	const user = await requireUser(fetch, url);

	const [projectsRes, nodesRes, metaRes] = await Promise.all([
		apiFetch(fetch, '/v1/projects?include=summary'),
		apiFetch(fetch, '/v1/nodes'),
		apiFetch(fetch, '/v1/system/meta')
	]);
	if (!projectsRes.ok) error(502, 'Could not load projects');
	const { projects } = (await projectsRes.json()) as { projects: Project[] };

	// Nodes and meta degrade gracefully: a partial shell beats an error page.
	const nodesBody = nodesRes.ok ? ((await nodesRes.json()) as NodesResponse) : null;
	const meta = metaRes.ok ? ((await metaRes.json()) as SystemMeta) : null;
	const nodes = nodesBody?.nodes ?? [];

	return {
		user: user,
		org: buildOrg(meta, projects.length, nodes.length),
		projects,
		nodes,
		nodesObservation: nodesBody?.observation ?? null
	};
};
