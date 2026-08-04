// The BFF's connection to the Go API: bearer-token fetch plus the session
// cookie that carries the token between browser and BFF. The token never
// reaches browser JavaScript — it lives in an httpOnly cookie and is attached
// server-side (here and in the /_api proxy).

import { dev } from '$app/environment';
import { env } from '$env/dynamic/private';
import type { Cookies } from '@sveltejs/kit';

export const API_URL = env.API_URL ?? 'http://localhost:7070';

export const SESSION_COOKIE = 'skali_session';

/** Fetch against the Go API with the bearer token attached. */
export function apiFetch(
	fetchFn: typeof fetch,
	token: string | null,
	path: string,
	init: RequestInit = {}
): Promise<Response> {
	const headers = new Headers(init.headers);
	if (token) headers.set('authorization', `Bearer ${token}`);
	if (init.body !== undefined && !headers.has('content-type')) {
		headers.set('content-type', 'application/json');
	}
	return fetchFn(`${API_URL}${path}`, { ...init, headers });
}

/**
 * Store the bearer token. The cookie expiry matches the session's expiry at
 * login time; the API extends sessions on use (sliding refresh), so the
 * cookie may outlive or undershoot the token slightly — hooks clears it on
 * the first 401 either way.
 */
export function setSessionCookie(cookies: Cookies, token: string, expiresAt: string): void {
	cookies.set(SESSION_COOKIE, token, {
		path: '/',
		httpOnly: true,
		sameSite: 'lax',
		secure: !dev,
		expires: new Date(expiresAt)
	});
}

export function clearSessionCookie(cookies: Cookies): void {
	cookies.delete(SESSION_COOKIE, { path: '/' });
}

/**
 * Headers that forward the browser's identity to the API, so sessions carry
 * the real device (session lists) and rate limits key on the real client IP —
 * not the BFF's. The API trusts X-Real-IP because it sits behind the BFF.
 */
export function clientMeta(
	request: Request,
	getClientAddress: () => string
): Record<string, string> {
	const headers: Record<string, string> = {};
	const ua = request.headers.get('user-agent');
	if (ua) headers['user-agent'] = ua;
	try {
		headers['x-real-ip'] = getClientAddress();
	} catch {
		// Not available outside a request context; the API falls back to the
		// connection address.
	}
	return headers;
}
