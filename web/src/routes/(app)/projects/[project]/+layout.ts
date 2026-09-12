// Project scope: resolve the route slug (the project name) against the shell
// load's project list, resolve `?env=` to an environment, and fetch the
// draft definition plus the environment's status projection.

import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import { servicesFromDefinition } from '$lib/models/service';
import type { DraftResponse } from '$lib/types/definition';
import type { Environment } from '$lib/types/project';
import type { EnvironmentStatus } from '$lib/types/status';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ params, url, parent, fetch }) => {
	const { projects } = await parent();
	const project = projects.find((p) => p.name === params.project);
	if (!project) error(404, 'Project not found');

	const envsRes = await apiFetch(fetch, `/v1/projects/${project.id}/environments`);
	if (!envsRes.ok) error(502, 'Could not load environments');
	const { environments } = (await envsRes.json()) as { environments: Environment[] };

	// Reading ?env= registers the search-param dependency: switching the
	// environment re-runs this load and everything below it. An unknown or
	// absent value falls back to production, then the first environment the
	// caller may read, then the first at all (a locked one renders as such).
	const requested = url.searchParams.get('env');
	const open = environments.filter((e) => e.access !== 'none');
	const env =
		environments.find((e) => e.name === requested) ??
		open.find((e) => e.name === 'production') ??
		open[0] ??
		environments[0] ??
		null;

	// A project may have no draft (404: zero services) and status may be
	// briefly unavailable; both degrade instead of erroring the whole scope.
	const [draftRes, statusRes] = await Promise.all([
		apiFetch(fetch, `/v1/projects/${project.id}/draft`),
		env ? apiFetch(fetch, `/v1/environments/${env.id}/status`) : Promise.resolve(null)
	]);
	const draft = draftRes.ok ? ((await draftRes.json()) as DraftResponse).draft : null;
	const definition = draft?.definition ?? null;
	const status = statusRes && statusRes.ok ? ((await statusRes.json()) as EnvironmentStatus) : null;

	return {
		project,
		environments,
		env,
		definition,
		// The draft's identity (hash, version, where it came from) for the
		// settings page; the definition above is what everything else reads.
		draft: draft
			? { version: draft.version, format: draft.format, source: draft.source, hash: draft.hash }
			: null,
		services: servicesFromDefinition(definition),
		status
	};
};
