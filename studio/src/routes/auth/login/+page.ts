import { redirect } from '@sveltejs/kit';
import { readUser } from '#lib/session.ts';
import { safeNext } from '#lib/safe-next.ts';
import type { PageLoad } from './$types';

export const load: PageLoad = async ({ fetch, url }) => {
	const next = safeNext(url.searchParams.get('next'));
	if (await readUser(fetch)) redirect(307, next);
	return { next, title: 'Sign in to Skali' };
};
