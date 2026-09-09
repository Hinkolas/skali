import { expect, test } from 'vitest';
import { readUser, requireUser } from '../src/lib/session';

test('only an invalid session is treated as signed out', async () => {
	expect(await readUser(async () => new Response('', { status: 401 }))).toBeNull();
	await expect(readUser(async () => new Response('', { status: 503 }))).rejects.toMatchObject({
		status: 503
	});
	await expect(
		readUser(async () => {
			throw new Error('offline');
		})
	).rejects.toMatchObject({ status: 503 });
});

test('authentication guards preserve deep links and environment selection', async () => {
	await expect(
		requireUser(
			async () => new Response('', { status: 401 }),
			new URL('https://skali.test/projects/shop?env=staging')
		)
	).rejects.toMatchObject({
		status: 307,
		location: '/auth/login?next=%2Fprojects%2Fshop%3Fenv%3Dstaging'
	});
});
