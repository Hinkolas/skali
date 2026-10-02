import { redirect } from '@sveltejs/kit';

export class ApiError extends Error {
	constructor(
		public status: number,
		public code: string,
		message: string
	) {
		super(message);
		this.name = 'ApiError';
	}
}

async function request<T>(
	method: string,
	path: string,
	body?: unknown,
	retried = false
): Promise<T> {
	let res: Response;
	try {
		res = await fetch(`/api${path}`, {
			method,
			headers: {
				'X-Requested-With': 'skali',
				...(body !== undefined ? { 'content-type': 'application/json' } : {})
			},
			credentials: 'same-origin',
			body: body !== undefined ? JSON.stringify(body) : undefined
		});
	} catch {
		throw new ApiError(0, 'network', 'The server is unreachable.');
	}
	if (res.status === 204) return undefined as T;
	const data = await res.json().catch(() => null);
	if (!res.ok) {
		const detail = data?.error;
		// Sudo mode: gated endpoints reject stale sessions with reauth_required.
		// Confirm identity once (concurrent failures share a single modal via
		// requireReauth) and transparently replay the original request — the
		// gate rejected it before the handler ran, so the replay is the first
		// real execution. `retried` stops a second rejection from looping.
		// Dynamic import: client → reauth store → ReauthModal → client is a cycle.
		if (res.status === 403 && detail?.code === 'reauth_required' && !retried) {
			const { requireReauth } = await import('$lib/stores/reauth.svelte');
			if (await requireReauth()) return request<T>(method, path, body, true);
		}
		// A 401 usually means the session died mid-use — a full navigation lands
		// on the login page. But invalid_credentials/invalid_code are user errors
		// (wrong password or TOTP code in settings flows), not a dead session.
		if (
			res.status === 401 &&
			detail?.code !== 'invalid_credentials' &&
			detail?.code !== 'invalid_code'
		) {
			window.location.href = loginDestination();
		}
		throw new ApiError(res.status, detail?.code ?? 'internal', detail?.message ?? res.statusText);
	}
	return data as T;
}

export const api = {
	get: <T>(path: string) => request<T>('GET', path),
	post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
	patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
	put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
	del: <T = void>(path: string) => request<T>('DELETE', path)
};

/** Raw responses for route loads that intentionally degrade on API errors. */
export async function apiFetch(
	fetchFn: typeof fetch,
	path: string,
	init: RequestInit = {}
): Promise<Response> {
	const headers = new Headers(init.headers);
	headers.set('X-Requested-With', 'skali');
	const response = await fetchFn(`/api${path}`, { ...init, headers, credentials: 'same-origin' });
	if (response.status === 401) redirect(307, loginDestination());
	return response;
}

export function loginDestination(): string {
	const next = window.location.pathname + window.location.search;
	return '/auth/login?next=' + encodeURIComponent(next);
}
