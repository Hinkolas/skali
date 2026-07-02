import type { Handle } from '@sveltejs/kit';
import { apiFetch, clearSessionCookie, SESSION_COOKIE } from '$lib/server/api';

// Resolves the session cookie into locals.user/locals.token by asking the Go
// API. A 401 clears the cookie (revoked/expired session); an unreachable API
// leaves it alone so a backend blip doesn't log everyone out.
export const handle: Handle = async ({ event, resolve }) => {
	event.locals.user = null;
	event.locals.token = null;

	const token = event.cookies.get(SESSION_COOKIE);
	if (token) {
		try {
			const res = await apiFetch(event.fetch, token, '/v1/auth/session');
			if (res.ok) {
				const data = await res.json();
				event.locals.user = data.user;
				event.locals.token = token;
			} else if (res.status === 401) {
				clearSessionCookie(event.cookies);
			}
		} catch {
			// API unreachable — treat as logged out for this request.
		}
	}

	return resolve(event);
};
