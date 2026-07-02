import { error } from '@sveltejs/kit';
import { findServiceTab } from '$lib/navigation';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ params, parent }) => {
	const { service } = await parent();
	const item = findServiceTab(service.type, params.tab);
	if (!item) error(404, 'Not found');
	return { title: item.label, icon: item.icon };
};
