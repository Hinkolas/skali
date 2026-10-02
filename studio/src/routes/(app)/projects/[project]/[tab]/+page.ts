import { error } from '@sveltejs/kit';
import { findProjectTab } from '$lib/navigation';
import type { PageLoad } from './$types';

export const load: PageLoad = ({ params }) => {
	const item = findProjectTab(params.tab);
	if (!item) error(404, 'Not found');
	return { title: item.label, icon: item.icon };
};
