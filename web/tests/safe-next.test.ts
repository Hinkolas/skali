import assert from 'node:assert/strict';
import { test } from 'vitest';
import { safeNext } from '../src/lib/safe-next.ts';

test('preserves local paths and environment queries', () => {
	for (const value of ['/', '/projects/shop?env=staging', '/account#sessions']) {
		assert.equal(safeNext(value), value);
	}
});

test('rejects destinations that a browser could interpret as another origin', () => {
	for (const value of [
		null,
		undefined,
		'',
		'https://evil.test',
		'//evil.test',
		'/\\evil.test',
		'/\n/evil.test',
		'/\t/evil.test',
		'/\r/evil.test'
	]) {
		assert.equal(safeNext(value), '/');
	}
});
