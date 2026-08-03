// Environment selection lives in a `?env=<name>` query param (Railway-style):
// no route restructure, shareable URLs. Pure helpers, no $app imports, so any
// component can call them with whatever `page` state it already has.

import type { Project } from '$lib/mock/types';

/**
 * The effective environment for a project page: the `?env=` param when it
 * names one of the project's environments, else the first (default) one.
 */
export function currentEnv(project: Project | undefined, url: URL): string | null {
	if (!project?.environments.length) return null;
	const q = url.searchParams.get('env');
	return q && project.environments.some((e) => e.name === q) ? q : project.environments[0].name;
}

/** Append `?env=` to a resolve()-built path; passthrough when env is null. */
export function withEnv(path: string, env: string | null): string {
	return env ? `${path}?env=${encodeURIComponent(env)}` : path;
}
