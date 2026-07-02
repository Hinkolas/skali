import { error } from '@sveltejs/kit';
import { getService } from '$lib/mock';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ params }) => {
	const service = await getService(params.project, params.service);
	if (!service) error(404, 'Service not found');
	return { service };
};
