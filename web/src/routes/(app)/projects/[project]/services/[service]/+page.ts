// Per-type overview data: the connection projection for databases and
// buckets, the environment's run feed for every type (deploys for
// applications, backups for the stateful ones), usage samples for
// applications.
// The service itself is selected from the project layout using the service
// route parameter.

import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import type { BucketConnection, DatabaseConnection } from '$lib/types/connections';
import type { EnvironmentMetrics, ProjectStorage, ServiceStorage } from '$lib/types/metrics';
import type { Run } from '$lib/types/runs';
import type { PageLoad } from './$types';

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

export const load: PageLoad = async ({ params, parent, fetch }) => {
	const { services, env, project } = await parent();
	const service = services.find((s) => s.key === params.service);
	if (!service) error(404, 'Service not found');

	if (!env)
		return {
			connection: null,
			bucketConnection: null,
			runs: null,
			metrics: null,
			storage: null,
			temporaryStorage: null
		};

	const storagePromise = apiFetch(fetch, `/v1/projects/${project.id}/storage`).then(
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

	// Backups are environment snapshots, so a database's or bucket's backup
	// history is the environment's run feed filtered to that kind.
	if (service.type === 'database') {
		const [res, runsRes, storage] = await Promise.all([
			apiFetch(fetch, `/v1/environments/${env.id}/databases/${service.key}/connection`),
			apiFetch(fetch, `/v1/environments/${env.id}/runs`),
			storagePromise
		]);
		return {
			connection: res.ok ? ((await res.json()) as DatabaseConnection) : null,
			bucketConnection: null,
			runs: runsRes.ok ? ((await runsRes.json()) as { runs: Run[] }).runs : null,
			metrics: null,
			...storage
		};
	}
	if (service.type === 'bucket') {
		const [res, runsRes, storage] = await Promise.all([
			apiFetch(fetch, `/v1/environments/${env.id}/buckets/${service.key}/connection`),
			apiFetch(fetch, `/v1/environments/${env.id}/runs`),
			storagePromise
		]);
		return {
			connection: null,
			bucketConnection: res.ok ? ((await res.json()) as BucketConnection) : null,
			runs: runsRes.ok ? ((await runsRes.json()) as { runs: Run[] }).runs : null,
			metrics: null,
			...storage
		};
	}
	const [res, metricsRes, storage] = await Promise.all([
		apiFetch(fetch, `/v1/environments/${env.id}/runs`),
		apiFetch(fetch, `/v1/environments/${env.id}/metrics?window=24h`),
		storagePromise
	]);
	return {
		connection: null,
		bucketConnection: null,
		runs: res.ok ? ((await res.json()) as { runs: Run[] }).runs : null,
		metrics: metricsRes.ok ? ((await metricsRes.json()) as EnvironmentMetrics) : null,
		...storage
	};
};
