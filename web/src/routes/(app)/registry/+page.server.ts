import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import type { RegistryImage } from '$lib/types/registry';
import type { PageServerLoad } from './$types';

// The mirror catalog is server-loaded per navigation; imports and deletes
// happen client-side through the /api proxy and re-run this load via
// invalidateAll(). A master without CLUSTER_ADDR runs no registry — the API
// answers 503 registry_disabled and the page renders that state instead of
// failing.
export const load: PageServerLoad = async ({ locals, fetch }) => {
	if (locals.user?.role !== 'admin') {
		error(403, 'You need the admin role to manage the registry');
	}

	const res = await apiFetch(fetch, locals.token, '/v1/registry/images');
	if (res.status === 503) {
		return { images: [] as RegistryImage[], disabled: true };
	}
	if (!res.ok) {
		error(res.status === 403 ? 403 : 502, 'Could not load the registry catalog');
	}
	const { images } = (await res.json()) as { images: RegistryImage[] };
	return { images, disabled: false };
};
