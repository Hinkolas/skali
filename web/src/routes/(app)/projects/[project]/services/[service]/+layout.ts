import { error } from '@sveltejs/kit';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ params, parent }) => {
	const { services } = await parent();
	const service = services.find((s) => s.key === params.service);
	if (!service) error(404, 'Service not found');
	return { service };
};
