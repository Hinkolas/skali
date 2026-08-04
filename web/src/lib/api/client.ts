// Thin client for the BFF proxy: relative /_api/v1/… calls, envelope-aware
// error handling. Components never talk to the Go API directly. The prefix
// is /_api because the production edge owns /api for the daemon itself.

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
		res = await fetch(`/_api${path}`, {
			method,
			headers: body !== undefined ? { 'content-type': 'application/json' } : undefined,
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
			window.location.href = '/auth/login';
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
