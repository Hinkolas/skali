// Auth store: authentication itself is handled by Go (session cookie + layout
// guards); this just exposes the current user to components and handles
// sign-out. Hydrated from layout data via setUser in the (app) layout.

import type { AuthUser } from '$lib/types/auth';

let user = $state<AuthUser | null>(null);

export const authState = {
	get user() {
		return user;
	}
};

/** Hydrate from layout data (called by the app layout). */
export function setUser(u: AuthUser | null) {
	user = u;
}

export async function signOut() {
	try {
		await fetch('/api/v1/auth/logout', {
			method: 'POST',
			headers: { 'X-Requested-With': 'skali' }
		});
	} catch {
		// Cookie may survive, but the login page will resolve the truth.
	}
	// Full navigation so every bit of client state is gone.
	window.location.href = '/auth/login';
}
