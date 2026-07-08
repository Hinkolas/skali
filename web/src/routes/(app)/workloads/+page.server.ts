import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import type { Workload } from '$lib/types/workloads';
import type { Node } from '$lib/types/nodes';
import type { PageServerLoad } from './$types';

// Workload reads always serve; writes share the registry's CLUSTER_ADDR
// gate (unified image handling needs the mirror). The registry list doubles
// as the gate probe so the page can say "disabled" instead of failing every
// mutation.
export const load: PageServerLoad = async ({ locals, fetch }) => {
	if (locals.user?.role !== 'admin') {
		error(403, 'You need the admin role to manage workloads');
	}

	const [wlRes, nodesRes, gateRes] = await Promise.all([
		apiFetch(fetch, locals.token, '/v1/workloads'),
		apiFetch(fetch, locals.token, '/v1/nodes'),
		apiFetch(fetch, locals.token, '/v1/registry/images')
	]);
	if (!wlRes.ok || !nodesRes.ok) {
		error(wlRes.status === 403 ? 403 : 502, 'Could not load workloads');
	}
	const { workloads } = (await wlRes.json()) as { workloads: Workload[] };
	const { nodes } = (await nodesRes.json()) as { nodes: Node[] };
	return { workloads, nodes, disabled: gateRes.status === 503 };
};
