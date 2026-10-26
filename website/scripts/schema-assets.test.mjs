import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { newestRelease } from './releases.mjs';
import { publishSchemas } from './schema-assets.mjs';

test('stable and prerelease schemas stay separate, with resolving identities', () => {
	const output = mkdtempSync(join(tmpdir(), 'skali-schema-assets-'));
	try {
		const tags = ['v0.1.2', 'v0.1.3-rc.9', 'v0.1.3-rc.10', '../unsafe', 'v0.0.1'];
		const source = (ref) => ({
			$id: 'https://skali.dev/schemas/v1/skali.schema.json',
			properties: ref === 'v0.1.2' ? { domain: {} } : { domain: {}, aliases: {} }
		});
		const git = (command, ...args) => {
			if (command === 'ls-tree') return args[1] === 'v0.0.1' ? '' : 'schemas/skali.schema.json\n';
			assert.equal(command, 'show');
			return JSON.stringify(source(args[0].split(':')[0]));
		};
		const ref = newestRelease(tags);
		assert.equal(ref, 'v0.1.2');
		assert.equal(newestRelease(tags.filter((tag) => tag.includes('-rc.'))), 'v0.1.3-rc.10');
		const result = publishSchemas(git, output, ref, tags);
		assert.deepEqual(result, { schemas: 1, releases: 3 });
		const read = (path) => JSON.parse(readFileSync(join(output, 'schemas', path), 'utf8'));
		assert.deepEqual(read('v1/skali.schema.json'), source('v0.1.2'));
		for (const tag of tags.slice(0, 3)) {
			const schema = read(`${tag}/skali.schema.json`);
			assert.equal(schema.$id, `https://skali.dev/schemas/${tag}/skali.schema.json`);
			assert.equal(schema.$comment, `Schema from Skali ${tag}.`);
			assert.deepEqual(schema.properties, source(tag).properties);
		}
		assert.equal(existsSync(join(output, 'unsafe')), false);
		assert.equal(read('releases.json').default, ref);
		assert.equal(read('releases.json').releases.length, 3);
		assert.throws(() => publishSchemas(git, output, 'v0.0.1', tags), /has no schemas/);
	} finally {
		rmSync(output, { recursive: true, force: true });
	}
});
