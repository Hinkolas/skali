// Per-type overview data: the connection projection for databases and
// buckets, the environment's run feed for applications. The service itself
// comes from the project layout's server data (found again here because
// universal-layout data is invisible to server loads).

import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import type { BucketConnection, DatabaseConnection } from '$lib/types/connections';
import type { ProjectStorage, ServiceStorage } from '$lib/types/metrics';
import type { Run } from '$lib/types/runs';
import type { PageServerLoad } from './$types';

// The stored storage key of one service: databases and buckets carry their
// status identity prefixes, application volumes use the bare key.
function storageKey(type: string, key: string): string {
	if (type === 'database') return `databases.${key}`;
	if (type === 'bucket') return `buckets.${key}`;
	return key;
}

// The primary storage kind of one service type. Applications additionally
// carry a `temporary` row under the same bare key, picked out separately.
function storageKind(type: string): string {
	if (type === 'database') return 'database';
	if (type === 'bucket') return 'bucket';
	return 'volume';
}

export const load: PageServerLoad = async ({ params, parent, locals, fetch }) => {
	const { services, env, project } = await parent();
	const service = services.find((s) => s.key === params.service);
	if (!service) error(404, 'Service not found');

	if (!env)
		return {
			connection: null,
			bucketConnection: null,
			runs: null,
			storage: null,
			temporaryStorage: null
		};

	const storagePromise = apiFetch(fetch, locals.token, `/v1/projects/${project.id}/storage`).then(
		async (
			res
		): Promise<{ storage: ServiceStorage | null; temporaryStorage: ServiceStorage | null }> => {
			if (!res.ok) return { storage: null, temporaryStorage: null };
			const payload = (await res.json()) as ProjectStorage;
			const key = storageKey(service.type, service.key);
			const rows = payload.services.filter(
				(s) => s.environment_id === env.id && s.service_key === key
			);
			return {
				storage: rows.find((s) => s.kind === storageKind(service.type)) ?? null,
				temporaryStorage: rows.find((s) => s.kind === 'temporary') ?? null
			};
		}
	);

	if (service.type === 'database') {
		const res = await apiFetch(
			fetch,
			locals.token,
			`/v1/environments/${env.id}/databases/${service.key}/connection`
		);
		return {
			connection: res.ok ? ((await res.json()) as DatabaseConnection) : null,
			bucketConnection: null,
			runs: null,
			...(await storagePromise)
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
			runs: null,
			...(await storagePromise)
		};
	}
	const res = await apiFetch(fetch, locals.token, `/v1/environments/${env.id}/runs`);
	return {
		connection: null,
		bucketConnection: null,
		runs: res.ok ? ((await res.json()) as { runs: Run[] }).runs : null,
		...(await storagePromise)
	};
};
