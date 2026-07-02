import { error } from '@sveltejs/kit';
import { getProject, listServices } from '$lib/mock';
import type { LayoutLoad } from './$types';

export const load: LayoutLoad = async ({ params }) => {
	const project = await getProject(params.project);
	if (!project) error(404, 'Project not found');
	return { project, services: await listServices(params.project) };
};
