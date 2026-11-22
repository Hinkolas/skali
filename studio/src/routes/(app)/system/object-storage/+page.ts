import { error } from '@sveltejs/kit';
import { apiFetch } from '#lib/api/client.ts';
import type { ObjectStore, ObjectStoreBuckets } from '#lib/types/objectstorage.ts';
import type { PageLoad } from './$types';

// Admin only, like every system page: the API already answers 403 to
// members, and the page refuses too so the sidebar's hiding is not the only
// line. An installation without the object-storage capability has no store
// (404), which the page renders as its empty state. The bucket list reads
// the master for usage; a failure there leaves the list out, not the page.
export const load: PageLoad = async ({ fetch, parent }) => {
	const { user } = await parent();
	if (user?.role !== 'admin') {
		error(403, 'You need the admin role to manage object storage');
	}
	const [res, bucketsRes] = await Promise.all([
		apiFetch(fetch, '/v1/system/object-storage'),
		apiFetch(fetch, '/v1/system/object-storage/buckets')
	]);
	if (res.status === 404) {
		return { store: null, inventory: null };
	}
	if (!res.ok) {
		error(res.status, 'Could not load the object store');
	}
	const { store } = (await res.json()) as { store: ObjectStore };
	return {
		store,
		inventory: bucketsRes.ok ? ((await bucketsRes.json()) as ObjectStoreBuckets) : null
	};
};
