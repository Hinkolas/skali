import assert from 'node:assert/strict';
import { test } from 'vitest';
import { describeUserAgent } from '../src/lib/useragent.ts';

test('recognizes the Skali CLI with its host', () => {
	assert.deepEqual(describeUserAgent('skali/v0.1.3-rc.3 (Acme-Mac-Mini)'), {
		kind: 'cli',
		label: 'Skali CLI v0.1.3-rc.3',
		host: 'Acme-Mac-Mini',
		mobile: false,
		tablet: false
	});
	assert.equal(describeUserAgent('skali/v0.1.3')?.host, null);
});

test('recognizes the Apple app on iPhone and iPad', () => {
	const phone = describeUserAgent('skali-ios/0.1.0 (iPhone; iOS 27.1)');
	assert.equal(phone?.kind, 'app');
	assert.equal(phone?.label, 'Skali for iOS');
	assert.equal(phone?.host, 'iPhone');
	assert.equal(phone?.tablet, false);

	const pad = describeUserAgent('skali-ios/0.1.0 (iPad; iPadOS 27.1)');
	assert.equal(pad?.label, 'Skali for iPadOS');
	assert.equal(pad?.tablet, true);
	assert.equal(pad?.mobile, true);
});

test('labels browsers by browser and system', () => {
	const cases: [string, string, boolean][] = [
		[
			'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.0 Safari/605.1.15',
			'Safari on macOS',
			false
		],
		[
			'Mozilla/5.0 (iPhone; CPU iPhone OS 27_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.0 Mobile/15E148 Safari/604.1',
			'Safari on iOS',
			true
		],
		[
			'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36 Edg/140.0',
			'Edge on Windows',
			false
		]
	];
	for (const [ua, label, mobile] of cases) {
		const device = describeUserAgent(ua);
		assert.equal(device?.kind, 'browser');
		assert.equal(device?.label, label);
		assert.equal(device?.mobile, mobile);
		assert.equal(device?.host, null);
	}
});

test('returns null for what it cannot name', () => {
	assert.equal(describeUserAgent(''), null);
	assert.equal(describeUserAgent('curl/8.7.1'), null);
});
