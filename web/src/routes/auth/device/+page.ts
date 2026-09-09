import { apiFetch } from '$lib/api/client';
import { requireUser } from '$lib/session';
import type { DeviceInfo } from '$lib/types/device';
import type { PageLoad } from './$types';

// Browser device authorization: the CLI sent the person here with
// ?code=XXXX-XXXX. The page needs a session (sign in first, then come
// back), looks the code up, and lets the person approve or deny it. The
// approve call goes through the API from the browser so the sudo
// interceptor can raise the reauth checkpoint.
export const load: PageLoad = async ({ url, fetch }) => {
	const user = await requireUser(fetch, url);
	const code = (url.searchParams.get('code') ?? '').trim();
	let info: DeviceInfo | null = null;
	let notFound = false;
	if (code) {
		const res = await apiFetch(fetch, `/v1/auth/device/codes/${encodeURIComponent(code)}`);
		if (res.ok) {
			info = (await res.json()) as DeviceInfo;
		} else {
			notFound = true;
		}
	}
	return { title: 'Authorize the CLI', user: user, code, info, notFound };
};
