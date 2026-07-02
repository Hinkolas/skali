import { fail, redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { apiFetch, clientMeta, setSessionCookie } from '$lib/server/api';

export const load: PageServerLoad = async ({ locals }) => {
	if (locals.user) redirect(302, '/');
};

// Login is a two-step form: `login` exchanges credentials for either a
// session (cookie + redirect) or a 2FA challenge (second step), `verify`
// exchanges challenge + TOTP code for the session. The bearer token only
// ever touches the BFF.

type ApiErrorBody = { error?: { code?: string; message?: string } };

function errorMessage(status: number, body: ApiErrorBody | null): string {
	switch (body?.error?.code) {
		case 'invalid_credentials':
			return 'Wrong email or password.';
		case 'invalid_code':
			return 'That code is not valid.';
		case 'invalid_token':
			return 'The code expired — sign in again.';
		case 'rate_limited':
			return 'Too many attempts. Wait a moment and try again.';
		default:
			return status >= 500 || status === 0
				? 'The server is unreachable. Try again in a moment.'
				: (body?.error?.message ?? 'Something went wrong.');
	}
}

export const actions: Actions = {
	login: async ({ request, cookies, fetch, getClientAddress }) => {
		const form = await request.formData();
		const email = String(form.get('email') ?? '').trim();
		const password = String(form.get('password') ?? '');
		if (!email || !password) {
			return fail(400, { message: 'Email and password are required.', email });
		}

		let res: Response;
		try {
			res = await apiFetch(fetch, null, '/v1/auth/login', {
				method: 'POST',
				body: JSON.stringify({ email, password }),
				headers: clientMeta(request, getClientAddress)
			});
		} catch {
			return fail(503, { message: errorMessage(0, null), email });
		}
		const body = await res.json().catch(() => null);
		if (!res.ok) {
			return fail(res.status, { message: errorMessage(res.status, body), email });
		}

		if (body.challenge) {
			return {
				step: 'totp' as const,
				challengeToken: body.challenge.token as string,
				email
			};
		}

		setSessionCookie(cookies, body.session.token, body.session.expires_at);
		redirect(303, '/');
	},

	verify: async ({ request, cookies, fetch, getClientAddress }) => {
		const form = await request.formData();
		const challengeToken = String(form.get('challenge_token') ?? '');
		const code = String(form.get('code') ?? '').trim();
		if (!challengeToken || !code) {
			return fail(400, {
				step: 'totp' as const,
				challengeToken,
				message: 'Enter the 6-digit code.'
			});
		}

		let res: Response;
		try {
			res = await apiFetch(fetch, null, '/v1/auth/2fa/verify', {
				method: 'POST',
				body: JSON.stringify({ challenge_token: challengeToken, code }),
				headers: clientMeta(request, getClientAddress)
			});
		} catch {
			return fail(503, { step: 'totp' as const, challengeToken, message: errorMessage(0, null) });
		}
		const body = await res.json().catch(() => null);
		if (!res.ok) {
			// An expired/burned challenge sends the user back to step one.
			const expired = body?.error?.code === 'invalid_token';
			return fail(res.status, {
				step: expired ? undefined : ('totp' as const),
				challengeToken: expired ? undefined : challengeToken,
				message: errorMessage(res.status, body)
			});
		}

		setSessionCookie(cookies, body.session.token, body.session.expires_at);
		redirect(303, '/');
	}
};
