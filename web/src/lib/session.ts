import { error, redirect } from '@sveltejs/kit';
import type { AuthUser } from '$lib/types/auth';

export async function readUser(fetchFn: typeof fetch): Promise<AuthUser | null> {
	let response: Response;
	try {
		response = await fetchFn('/api/v1/auth/session', { credentials: 'same-origin' });
	} catch {
		error(503, 'The server is unreachable. Try again in a moment.');
	}
	if (response.status === 401) return null;
	if (!response.ok) error(503, 'Could not check your session. Try again in a moment.');
	return (await response.json()).user as AuthUser;
}

export async function requireUser(fetchFn: typeof fetch, url: URL): Promise<AuthUser> {
	const user = await readUser(fetchFn);
	if (!user) redirect(307, '/auth/login?next=' + encodeURIComponent(url.pathname + url.search));
	return user;
}
