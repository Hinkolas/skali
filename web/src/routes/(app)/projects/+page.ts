// The overview is the one page that shows per-environment health, so it is
// the one page that asks for it. The summary is the kernel's cached verdict
// per environment (one batch read on the daemon), so awaiting it costs about
// as much as the plain list. A failure degrades to the shell's plain list
// (grey dots, no counts) instead of an error page.
//
// The key `projects` deliberately shadows the shell's in merged page.data
// while this page is open: same list, richer shape. Breadcrumbs and the
// command palette read either happily.

import { apiFetch } from '$lib/api/client';
import type { Project } from '$lib/types/project';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch, parent }) => {
	// Fired before parent() so it runs alongside the shell loads instead of
	// queuing behind them.
	const res = await apiFetch(fetch, '/v1/projects?include=summary');
	if (res.ok) {
		const { projects } = (await res.json()) as { projects: Project[] };
		return { projects };
	}
	const { projects } = await parent();
	return { projects };
};
