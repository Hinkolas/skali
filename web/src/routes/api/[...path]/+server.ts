// Catch-all BFF proxy: the browser calls relative /api/v1/… URLs, this
// forwards them to the Go API with the bearer token from the httpOnly session
// cookie attached. The token never reaches browser JavaScript.

import { json, type RequestHandler } from '@sveltejs/kit';
import { apiFetch, clientMeta } from '$lib/server/api';

// Session-issuing endpoints are never proxied — their responses contain raw
// bearer tokens, which must stay between the BFF and the API. v1/auth/reauth
// does NOT belong here: it returns 204 (no token) and the client-side sudo
// interceptor depends on reaching it through this proxy.
const DENIED_PREFIXES = ['v1/auth/login', 'v1/auth/2fa/verify'];

const proxy: RequestHandler = async ({ params, request, url, locals, fetch, getClientAddress }) => {
	const path = params.path ?? '';
	if (DENIED_PREFIXES.some((p) => path === p || path.startsWith(p + '/'))) {
		return json({ error: { code: 'not_found', message: 'not found' } }, { status: 404 });
	}
	if (!locals.token) {
		return json(
			{ error: { code: 'invalid_token', message: 'not authenticated' } },
			{ status: 401 }
		);
	}

	// Forward method, query, and body; drop the browser's cookies and headers
	// except content-type and client identity (apiFetch sets authorization).
	const method = request.method;
	const fwdHeaders = clientMeta(request, getClientAddress);
	const init: RequestInit = { method, headers: fwdHeaders };
	if (method !== 'GET' && method !== 'HEAD') {
		init.body = await request.arrayBuffer();
		const contentType = request.headers.get('content-type');
		if (contentType) fwdHeaders['content-type'] = contentType;
	}
	const upstream = await apiFetch(fetch, locals.token, `/${path}${url.search}`, init);

	const headers = new Headers();
	const contentType = upstream.headers.get('content-type');
	if (contentType) headers.set('content-type', contentType);
	const body = upstream.status === 204 ? null : await upstream.arrayBuffer();
	return new Response(body, { status: upstream.status, headers });
};

export const GET = proxy;
export const POST = proxy;
export const PATCH = proxy;
export const PUT = proxy;
export const DELETE = proxy;
