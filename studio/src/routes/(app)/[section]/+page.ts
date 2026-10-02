import { error } from '@sveltejs/kit';
import { findOrgSection } from '$lib/navigation';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ params, parent }) => {
	const item = findOrgSection(params.section);
	if (!item) error(404, 'Not found');
	// Admin-only sections (system) refuse members instead of rendering a
	// stub they could never use.
	if (item.adminOnly) {
		const { user } = await parent();
		if (user?.role !== 'admin') error(403, 'You need the admin role for this section');
	}
	return { title: item.label, icon: item.icon };
};
