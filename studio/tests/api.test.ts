import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { api, apiFetch } from '../src/lib/api/client';

const modal = vi.hoisted(() => ({ push: vi.fn() }));
vi.mock('$lib/stores/modal.svelte', () => ({ modal }));
vi.mock('$lib/components/auth/ReauthModal.svelte', () => ({
	default: {},
	modalOptions: {}
}));

const rejection = () =>
	Response.json({ error: { code: 'reauth_required', message: 'Confirm access' } }, { status: 403 });

beforeEach(() => {
	vi.clearAllMocks();
	vi.stubGlobal('window', {
		location: { href: '', pathname: '/projects/shop', search: '?env=staging' }
	});
});
afterEach(() => vi.unstubAllGlobals());

test('concurrent gated requests share one prompt and each retry once', async () => {
	let confirm!: (accepted: boolean) => void;
	modal.push.mockReturnValue({ result: new Promise<boolean>((resolve) => (confirm = resolve)) });
	const attempts = new Map<string, number>();
	const fetchMock = vi.fn(async (path: string) => {
		const attempt = (attempts.get(path) ?? 0) + 1;
		attempts.set(path, attempt);
		return attempt === 1 ? rejection() : Response.json({ saved: true });
	});
	vi.stubGlobal('fetch', fetchMock);
	const first = api.post('/v1/first', { value: 'one' });
	const second = api.post('/v1/second', { value: 'two' });
	await vi.waitFor(() => expect(modal.push).toHaveBeenCalledTimes(1));
	confirm(true);
	expect(await Promise.all([first, second])).toEqual([{ saved: true }, { saved: true }]);
	expect([...attempts.values()]).toEqual([2, 2]);
	expect(fetchMock).toHaveBeenCalledWith(
		'/api/v1/first',
		expect.objectContaining({
			credentials: 'same-origin',
			headers: expect.objectContaining({ 'X-Requested-With': 'skali' }),
			body: JSON.stringify({ value: 'one' })
		})
	);
});

test('a second reauthentication rejection cannot loop', async () => {
	modal.push.mockReturnValue({ result: Promise.resolve(true) });
	const fetchMock = vi.fn(async () => rejection());
	vi.stubGlobal('fetch', fetchMock);
	await expect(api.post('/v1/gated')).rejects.toMatchObject({ code: 'reauth_required' });
	expect(fetchMock).toHaveBeenCalledTimes(2);
	expect(modal.push).toHaveBeenCalledTimes(1);
});

test('cancelled reauthentication does not replay the mutation', async () => {
	modal.push.mockReturnValue({ result: Promise.resolve(false) });
	const fetchMock = vi.fn(async () => rejection());
	vi.stubGlobal('fetch', fetchMock);
	await expect(api.post('/v1/gated')).rejects.toMatchObject({ code: 'reauth_required' });
	expect(fetchMock).toHaveBeenCalledTimes(1);
});

test('invalid sessions redirect with path and query while credential errors stay local', async () => {
	vi.stubGlobal(
		'fetch',
		vi.fn(async () => Response.json({ error: { code: 'invalid_credentials' } }, { status: 401 }))
	);
	await expect(api.post('/v1/auth/reauth')).rejects.toMatchObject({ code: 'invalid_credentials' });
	expect(window.location.href).toBe('');
	vi.stubGlobal(
		'fetch',
		vi.fn(async () => Response.json({ error: { code: 'invalid_token' } }, { status: 401 }))
	);
	await expect(api.get('/v1/projects')).rejects.toMatchObject({ code: 'invalid_token' });
	expect(window.location.href).toBe('/auth/login?next=%2Fprojects%2Fshop%3Fenv%3Dstaging');
});

test('route loads use the supplied fetch and preserve unavailable-data responses', async () => {
	const response = new Response('', { status: 503 });
	const fetchMock = vi.fn(async () => response);
	expect(await apiFetch(fetchMock, '/v1/nodes')).toBe(response);
	expect(fetchMock).toHaveBeenCalledWith(
		'/api/v1/nodes',
		expect.objectContaining({ credentials: 'same-origin' })
	);
});
