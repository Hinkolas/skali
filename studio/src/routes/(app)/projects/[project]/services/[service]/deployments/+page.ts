// Deployments moved up to the project: a deployment rolls out the whole
// environment, so every service showed the same list here. Old links land
// on the project page in the same environment.

import { redirect } from '@sveltejs/kit';
import { resolve } from '$app/paths';
import { withEnv } from '$lib/urls';
import type { PageLoad } from './$types';

export const load: PageLoad = ({ params, url }) => {
	redirect(
		308,
		withEnv(
			resolve('/(app)/projects/[project]/deployments', { project: params.project }),
			url.searchParams.get('env')
		)
	);
};
