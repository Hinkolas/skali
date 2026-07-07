import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import type { Node, NodeContainer, NodeImage, NodeVolume } from '$lib/types/nodes';
import type { PageServerLoad } from './$types';

// The whole engine surface is server-loaded per navigation; mutations happen
// client-side through the /api proxy and re-run this load via
// invalidateAll(). The page also re-runs it on an interval. Filtering is
// client-side over the full dataset — the API's ?node= filter exists for
// other clients.
export const load: PageServerLoad = async ({ locals, fetch }) => {
	if (locals.user?.role !== 'admin') {
		error(403, 'You need the admin role to manage containers');
	}

	const [containersRes, imagesRes, volumesRes, nodesRes] = await Promise.all([
		apiFetch(fetch, locals.token, '/v1/containers'),
		apiFetch(fetch, locals.token, '/v1/images'),
		apiFetch(fetch, locals.token, '/v1/volumes'),
		apiFetch(fetch, locals.token, '/v1/nodes')
	]);
	for (const res of [containersRes, imagesRes, volumesRes, nodesRes]) {
		if (!res.ok) {
			error(res.status === 403 ? 403 : 502, 'Could not load the container surface');
		}
	}

	const { containers } = (await containersRes.json()) as { containers: NodeContainer[] };
	const { images } = (await imagesRes.json()) as { images: NodeImage[] };
	const { volumes } = (await volumesRes.json()) as { volumes: NodeVolume[] };
	const { nodes } = (await nodesRes.json()) as { nodes: Node[] };
	return { containers, images, volumes, nodes };
};
