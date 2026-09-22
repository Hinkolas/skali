// The overview is the one page that shows per-environment health, so it is
// the one page that asks for it. The summary is returned as a promise, not
// awaited: SvelteKit does not await top-level promises from a universal
// load, so the page paints from the shell's plain project list (names,
// timestamps) as soon as the shell is ready and fills in environments,
// service counts and health when the summary settles (see deferred()).
// The summary is the kernel's cached verdict per environment, one batch
// read on the daemon, so the fill-in is quick. A failure resolves to the
// plain list instead of an error page.
//
// The key is `summary`, not `projects`: the shell's `projects` stays an
// array in merged page.data for the breadcrumb switcher and the palette.

import { apiFetch } from '$lib/api/client';
import type { Project } from '$lib/types/project';
import type { PageLoad } from './$types';

export const load: PageLoad = ({ fetch, parent }) => {
	const summary = (async (): Promise<Project[]> => {
		const res = await apiFetch(fetch, '/v1/projects?include=summary');
		if (res.ok) return ((await res.json()) as { projects: Project[] }).projects;
		return (await parent()).projects;
	})();
	return { summary };
};
