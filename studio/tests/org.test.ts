import { expect, test } from 'vitest';
import { buildOrg } from '../src/lib/models/org';

test('the org name prefers the instance name, then the host, then skali', () => {
	expect(buildOrg({ version: '1.2.3', name: 'Acme' }, 'skali.acme.test', 3, 2).name).toBe('Acme');
	expect(buildOrg({ version: '1.2.3' }, 'skali.acme.test', 3, 2).name).toBe('skali.acme.test');
	expect(buildOrg(null, '', 0, null).name).toBe('skali');
});

test('the org line carries the update and the counts it was given', () => {
	const org = buildOrg(
		{ version: '1.2.3', update_available: { version: '1.3.0' } as never },
		'host',
		3,
		null
	);
	expect(org.version).toBe('1.2.3');
	expect(org.update_available).toBe('1.3.0');
	expect(org.project_count).toBe(3);
	// null: the caller may not see nodes at all, unlike zero observed nodes.
	expect(org.node_count).toBeNull();
	expect(buildOrg(null, 'host', 3, 0).node_count).toBe(0);
});
