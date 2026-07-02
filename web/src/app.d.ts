import type { AuthUser } from '$lib/types/auth';

// See https://svelte.dev/docs/kit/types#app.d.ts
// for information about these interfaces
declare global {
	namespace App {
		// interface Error {}
		interface Locals {
			/** Authenticated user, resolved from the session cookie by hooks.server.ts. */
			user: AuthUser | null;
			/** The API bearer token behind the cookie; server-side only. */
			token: string | null;
		}
		// interface PageData {}
		// interface PageState {}
		// interface Platform {}
	}
}

export {};
