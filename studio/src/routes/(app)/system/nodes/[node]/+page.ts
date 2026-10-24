import { error } from '@sveltejs/kit';
import { resolve } from '$app/paths';
import { apiFetch } from '$lib/api/client';
import type { NodeMetrics, NodesStorage } from '$lib/types/metrics';
import type { UpdateStatus } from '$lib/types/updates';
import type { PageLoad } from './$types';

// One node, admin only like every system page. The node itself comes from
// the shell's node list; this load adds its usage series (24h seeds the
// window picker, the page polls the rest), its newest storage sample, and
// the update document, which is where the host agent reports its version.
// Each extra degrades to null: the page still shows what was observed.
export const load: PageLoad = async ({ fetch, params, parent }) => {
	const { user, nodes } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to see nodes');
	}
	const node = nodes?.find((n) => n.name === params.node);
	if (!node) {
		error(404, 'Node not found');
	}
	const [metricsRes, storageRes, updatesRes] = await Promise.all([
		apiFetch(fetch, '/v1/nodes/metrics?window=24h'),
		apiFetch(fetch, '/v1/nodes/storage'),
		apiFetch(fetch, '/v1/system/updates')
	]);
	const storage = storageRes.ok ? ((await storageRes.json()) as NodesStorage) : null;
	const updates = updatesRes.ok ? ((await updatesRes.json()) as UpdateStatus) : null;
	return {
		node,
		metrics: metricsRes.ok ? ((await metricsRes.json()) as NodeMetrics) : null,
		storage: storage?.nodes.find((n) => n.name === node.name) ?? null,
		agentVersion: updates?.nodes.find((n) => n.name === node.name)?.agent_version ?? null,
		crumbs: [
			{ label: 'System', href: resolve('/(app)/system') },
			{ label: 'Nodes', href: resolve('/(app)/system/nodes') },
			{ label: node.name }
		]
	};
};
