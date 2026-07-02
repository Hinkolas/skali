import { error } from '@sveltejs/kit';
import { findOrgSection } from '$lib/navigation';
import type { PageLoad } from './$types';

export const load: PageLoad = ({ params }) => {
	const item = findOrgSection(params.section);
	if (!item) error(404, 'Not found');
	return { title: item.label, icon: item.icon };
};
