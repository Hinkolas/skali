import { redirect, type RequestHandler } from '@sveltejs/kit';
import { apiFetch, clearSessionCookie } from '$lib/server/api';

export const POST: RequestHandler = async ({ cookies, locals, fetch }) => {
	if (locals.token) {
		try {
			await apiFetch(fetch, locals.token, '/v1/auth/logout', { method: 'POST' });
		} catch {
			// The cookie is cleared regardless; the API session sweeper
			// collects leftovers.
		}
	}
	clearSessionCookie(cookies);
	redirect(303, '/auth/login');
};
