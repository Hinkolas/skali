// Thin client for the BFF proxy: relative /api/v1/… calls, envelope-aware
// error handling. Components never talk to the Go API directly.

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

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
	let res: Response;
	try {
		res = await fetch(`/api${path}`, {
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
	del: <T = void>(path: string) => request<T>('DELETE', path)
};
