import { expect, test } from 'vitest';
import { connectedStat } from '../src/lib/models/claims';
import type { ServiceView } from '../src/lib/models/service';
import type { ServiceStatus } from '../src/lib/types/status';

const apps = [{ key: 'web' }, { key: 'worker' }] as unknown as ServiceView[];
const live = (key: string) =>
	key === 'web' ? ({ health: 'healthy' } as unknown as ServiceStatus) : undefined;

test('the connected tile counts healthy dependents once status is known', () => {
	const stat = connectedStat(apps, live, 'private network');
	expect(stat.value).toBe('2');
	expect(stat.unit).toBe('apps');
	expect(stat.note).toBe('1/2 healthy · private network');
});

test('the connected tile holds off the healthy count while status is pending', () => {
	expect(connectedStat(apps, () => undefined, 'keys injected', true).note).toBe(
		'health pending · keys injected'
	);
	// Nothing depends on it: no health to wait for either way.
	expect(connectedStat([], () => undefined, 'keys injected', true).note).toBe(
		'no application depends on it'
	);
});
