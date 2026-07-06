// Singleflight for the sudo-mode prompt: when several gated API calls fail
// with reauth_required at once, they all share one ReauthModal and one
// promise — every waiter replays its original request after a single confirm.

import { modal } from '$lib/stores/modal.svelte';
import ReauthModal, {
	modalOptions as reauthModalOptions
} from '$lib/components/auth/ReauthModal.svelte';

let inflight: Promise<boolean> | null = null;

/** Prompt the user to confirm their identity; resolves true on success,
 *  false when they dismiss (Esc/backdrop/Cancel). */
export function requireReauth(): Promise<boolean> {
	if (!inflight) {
		inflight = modal
			.open<boolean>(ReauthModal, {}, reauthModalOptions)
			.result.then((ok) => ok === true)
			.finally(() => {
				inflight = null;
			});
	}
	return inflight;
}
