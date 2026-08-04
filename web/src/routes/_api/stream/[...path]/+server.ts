// SSE pass-through proxy: the buffered catch-all at /_api/[...path] reads the
// whole upstream body and therefore cannot serve event streams. This route
// pipes the upstream body through untouched. The literal `stream` segment
// wins over the catch-all sibling, so /_api/stream/v1/... lands here.

import { json, type RequestHandler } from '@sveltejs/kit';
import { apiFetch, clientMeta } from '$lib/server/api';

export const GET: RequestHandler = async ({
	params,
	request,
	url,
	locals,
	fetch,
	getClientAddress
}) => {
	const path = params.path ?? '';
	// Only the API's stream endpoints are reachable here; everything else
	// belongs to the buffered proxy.
	if (!path.endsWith('/stream')) {
		return json({ error: { code: 'not_found', message: 'not found' } }, { status: 404 });
	}
	if (!locals.token) {
		return json(
			{ error: { code: 'invalid_token', message: 'not authenticated' } },
			{ status: 401 }
		);
	}

	const headers = clientMeta(request, getClientAddress);
	const lastEventID = request.headers.get('last-event-id');
	if (lastEventID) headers['last-event-id'] = lastEventID;

	// request.signal aborts the upstream fetch when the browser disconnects,
	// so the Go daemon's stream goroutine ends instead of leaking.
	const upstream = await apiFetch(fetch, locals.token, `/${path}${url.search}`, {
		headers,
		signal: request.signal
	});

	if (!upstream.headers.get('content-type')?.startsWith('text/event-stream')) {
		// Error envelopes and other non-stream responses are small; buffer
		// and pass them through like the regular proxy.
		const body = upstream.status === 204 ? null : await upstream.arrayBuffer();
		const responseHeaders = new Headers();
		const contentType = upstream.headers.get('content-type');
		if (contentType) responseHeaders.set('content-type', contentType);
		return new Response(body, { status: upstream.status, headers: responseHeaders });
	}

	return new Response(upstream.body, {
		status: upstream.status,
		headers: {
			'content-type': 'text/event-stream',
			'cache-control': 'no-store',
			'x-accel-buffering': 'no'
		}
	});
};
