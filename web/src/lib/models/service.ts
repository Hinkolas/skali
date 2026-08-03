// The service view-model: one flat list of a project's services synthesized
// from the compiled definition. Health is not baked in here; it is looked up
// live by (type, key) against the environment status (types/status.ts).

import type {
	Application,
	BucketClaim,
	DatabaseClaim,
	ProjectDefinition
} from '$lib/types/definition';

export type ServiceViewType = 'application' | 'database' | 'bucket';

interface ServiceViewBase {
	/**
	 * Bare key inside its collection, e.g. "web" or "main". This is also the
	 * `key` reported by /status services[] (which discriminates by `type`)
	 * and the route slug (the manifest schema keeps keys unique enough).
	 */
	key: string;
	name: string;
	dependencies: string[];
}

export interface ApplicationView extends ServiceViewBase {
	type: 'application';
	config: Application;
}

export interface DatabaseView extends ServiceViewBase {
	type: 'database';
	config: DatabaseClaim;
}

export interface BucketView extends ServiceViewBase {
	type: 'bucket';
	config: BucketClaim;
}

export type ServiceView = ApplicationView | DatabaseView | BucketView;

/**
 * Flatten a compiled definition into the service list the sidebar, cards,
 * and tabs render. Applications first, then databases, then buckets, each
 * group sorted by key. Returns [] for a project without a draft.
 */
export function servicesFromDefinition(definition: ProjectDefinition | null): ServiceView[] {
	if (!definition) return [];
	const dependencies = definition.dependencies ?? {};
	const services: ServiceView[] = [];
	for (const [key, config] of sorted(definition.applications)) {
		services.push({
			type: 'application',
			key,
			name: key,
			// Dotted refs like "databases.main", keyed by "applications.<key>".
			dependencies: dependencies[`applications.${key}`] ?? [],
			config
		});
	}
	for (const [key, config] of sorted(definition.databases)) {
		services.push({ type: 'database', key, name: key, dependencies: [], config });
	}
	for (const [key, config] of sorted(definition.buckets)) {
		services.push({ type: 'bucket', key, name: key, dependencies: [], config });
	}
	return services;
}

function sorted<T>(record: Record<string, T> | undefined): [string, T][] {
	return Object.entries(record ?? {}).sort(([a], [b]) => a.localeCompare(b));
}
