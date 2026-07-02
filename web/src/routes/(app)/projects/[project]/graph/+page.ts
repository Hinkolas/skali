import { getProjectGraph } from '$lib/mock';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ params }) => {
	return { graph: await getProjectGraph(params.project) };
};
