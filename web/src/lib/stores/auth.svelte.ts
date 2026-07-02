// Auth store: authentication itself is server-side (session cookie + layout
// guards); this just exposes the current user to components and handles
// sign-out. Hydrated from layout data via setUser in the (app) layout.

import type { AuthUser } from '$lib/types/auth';

let user = $state<AuthUser | null>(null);

export const authState = {
	get user() {
		return user;
	}
};

/** Hydrate from server load data (called by the app layout). */
export function setUser(u: AuthUser | null) {
	user = u;
}

export async function signOut() {
	try {
		await fetch('/auth/logout', { method: 'POST' });
	} catch {
		// Cookie may survive, but the login page will resolve the truth.
	}
	// Full navigation so every bit of client state is gone.
	window.location.href = '/auth/login';
}
