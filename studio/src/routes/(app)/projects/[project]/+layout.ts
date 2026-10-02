// Project scope: resolve the route slug (the project name) against the shell
// load's project list, resolve `?env=` to an environment, and fetch the
// environments (with their cached summary) and the draft definition. The
// environment's live status is not loaded here: the status stream the
// layout component opens delivers the full document as its first event,
// and consumers show a pending state until it lands (envStatus.pending).

import { error } from '@sveltejs/kit';
import { apiFetch } from '$lib/api/client';
import { servicesFromDefinition } from '$lib/models/service';
import type { DraftResponse } from '$lib/types/definition';
import type { Environment } from '$lib/types/project';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ params, url, parent, fetch }) => {
	const { projects } = await parent();
	const project = projects.find((p) => p.name === params.project);
	if (!project) error(404, 'Project not found');

	// Environments and the draft need only the project id, so they leave
	// together. The summary rides along: each environment's state and cached
	// health, so pages listing environments never ask for status per row.
	const [envsRes, draftRes] = await Promise.all([
		apiFetch(fetch, `/v1/projects/${project.id}/environments?include=summary`),
		apiFetch(fetch, `/v1/projects/${project.id}/draft`)
	]);
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

	// A project may have no draft (404: zero services); it degrades instead
	// of erroring the whole scope.
	const draft = draftRes.ok ? ((await draftRes.json()) as DraftResponse).draft : null;
	const definition = draft?.definition ?? null;

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
		services: servicesFromDefinition(definition)
	};
};
