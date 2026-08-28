import { redirect } from '@sveltejs/kit';
import { apiFetch } from '$lib/server/api';
import type { DeviceInfo } from '$lib/types/device';
import type { PageServerLoad } from './$types';

// Browser device authorization: the CLI sent the person here with
// ?code=XXXX-XXXX. The page needs a session (sign in first, then come
// back), looks the code up, and lets the person approve or deny it. The
// approve call goes through the BFF from the browser so the sudo
// interceptor can raise the reauth checkpoint.
export const load: PageServerLoad = async ({ locals, url, fetch }) => {
	if (!locals.user) {
		redirect(302, '/auth/login?next=' + encodeURIComponent(url.pathname + url.search));
	}
	const code = (url.searchParams.get('code') ?? '').trim();
	let info: DeviceInfo | null = null;
	let notFound = false;
	if (code) {
		const res = await apiFetch(
			fetch,
			locals.token,
			`/v1/auth/device/codes/${encodeURIComponent(code)}`
		);
		if (res.ok) {
			info = (await res.json()) as DeviceInfo;
		} else {
			notFound = true;
		}
	}
	return { title: 'Authorize the CLI', user: locals.user, code, info, notFound };
};
