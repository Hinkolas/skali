// Per-type overview data: the connection projection for databases and
// buckets, the environment's run feed for applications. The service itself
// comes from the project layout's server data (found again here because
// universal-layout data is invisible to server loads).

import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import type { BucketConnection, DatabaseConnection } from '$lib/types/connections';
import type { Run } from '$lib/types/runs';
import type { PageServerLoad } from './$types';

export const load: PageServerLoad = async ({ params, parent, locals, fetch }) => {
	const { services, env } = await parent();
	const service = services.find((s) => s.key === params.service);
	if (!service) error(404, 'Service not found');

	if (!env) return { connection: null, bucketConnection: null, runs: null };

	if (service.type === 'database') {
		const res = await apiFetch(
			fetch,
			locals.token,
			`/v1/environments/${env.id}/databases/${service.key}/connection`
		);
		return {
			connection: res.ok ? ((await res.json()) as DatabaseConnection) : null,
			bucketConnection: null,
			runs: null
		};
	}
	if (service.type === 'bucket') {
		const res = await apiFetch(
			fetch,
			locals.token,
			`/v1/environments/${env.id}/buckets/${service.key}/connection`
		);
		return {
			connection: null,
			bucketConnection: res.ok ? ((await res.json()) as BucketConnection) : null,
			runs: null
		};
	}
	const res = await apiFetch(fetch, locals.token, `/v1/environments/${env.id}/runs`);
	return {
		connection: null,
		bucketConnection: null,
		runs: res.ok ? ((await res.json()) as { runs: Run[] }).runs : null
	};
};
