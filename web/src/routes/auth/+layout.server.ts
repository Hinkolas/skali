import type { LayoutServerLoad } from './$types';
import { API_URL } from '$lib/server/api';

// The daemon stamps Skali-Version on every response, /healthz included, so
// the sign-in page can show the instance version without a session. An
// unreachable API only hides the version; the page itself still renders.
export const load: LayoutServerLoad = async ({ fetch }) => {
	let version: string | null = null;
	try {
		const res = await fetch(`${API_URL}/healthz`, { signal: AbortSignal.timeout(2000) });
		version = res.headers.get('skali-version');
	} catch {
		// API down: the login action will surface a real error on submit.
	}
	return { version };
};
