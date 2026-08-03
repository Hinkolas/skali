// Project scope: resolve the route slug (the project name) against the shell
// load's project list, resolve `?env=` to an environment, and fetch the
// draft definition plus the environment's status projection.

import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import { servicesFromDefinition } from '$lib/models/service';
import type { DraftResponse } from '$lib/types/definition';
import type { Environment } from '$lib/types/project';
import type { EnvironmentStatus } from '$lib/types/status';
import type { LayoutServerLoad } from './$types';

export const load: LayoutServerLoad = async ({ params, url, parent, locals, fetch }) => {
	const { projects } = await parent();
	const project = projects.find((p) => p.name === params.project);
	if (!project) error(404, 'Project not found');

	const envsRes = await apiFetch(fetch, locals.token, `/v1/projects/${project.id}/environments`);
	if (!envsRes.ok) error(502, 'Could not load environments');
	const { environments } = (await envsRes.json()) as { environments: Environment[] };

	// Reading ?env= registers the search-param dependency: switching the
	// environment re-runs this load and everything below it. An unknown or
	// absent value falls back to production, then the first environment.
	const requested = url.searchParams.get('env');
	const env =
		environments.find((e) => e.name === requested) ??
		environments.find((e) => e.name === 'production') ??
		environments[0] ??
		null;

	// A project may have no draft (404: zero services) and status may be
	// briefly unavailable; both degrade instead of erroring the whole scope.
	const [draftRes, statusRes] = await Promise.all([
		apiFetch(fetch, locals.token, `/v1/projects/${project.id}/draft`),
		env ? apiFetch(fetch, locals.token, `/v1/environments/${env.id}/status`) : Promise.resolve(null)
	]);
	const definition = draftRes.ok
		? ((await draftRes.json()) as DraftResponse).draft.definition
		: null;
	const status = statusRes && statusRes.ok ? ((await statusRes.json()) as EnvironmentStatus) : null;

	return {
		project,
		environments,
		env,
		definition,
		services: servicesFromDefinition(definition),
		status
	};
};
