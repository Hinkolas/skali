import { expect, test } from 'vitest';
import { deploymentSummary, serviceChanges } from '../src/lib/models/rundeployment';
import type { Run, RunDeployment } from '../src/lib/types/runs';

function run(id: string, kind: Run['kind'], deployment?: RunDeployment): Run {
	return {
		id,
		kind,
		project_id: 'p',
		environment_id: 'e',
		actor: 'ada',
		status: 'succeeded',
		created_at: '2026-10-19T10:00:00Z',
		started_at: '2026-10-19T10:00:00Z',
		finished_at: '2026-10-19T10:01:00Z',
		deployment
	};
}

test('deploymentSummary says what a deployment changed, by action, values counted', () => {
	const summary = deploymentSummary(
		run('d', 'deployment', {
			revision: '3f9a1c2d4e5f',
			changes: [
				{ service: 'applications.web', action: 'update', detail: 'configuration changed' },
				{ service: 'applications.worker', action: 'update' },
				{ service: 'buckets.assets', action: 'remove', destructive: true },
				{ service: 'databases.main', action: 'create' }
			],
			values: [
				{ name: 'API_KEY', action: 'create' },
				{ name: 'OLD', action: 'remove' },
				{ name: 'OLD', action: 'prune' }
			]
		})
	);
	// A value removed from the definition and pruned from the store counts once.
	expect(summary).toBe('web, worker updated · main created · assets removed · 2 values changed');
});

test('deploymentSummary names a promotion source, a rollback target, and restarts', () => {
	expect(
		deploymentSummary(
			run('p', 'deployment', {
				from_environment: 'staging',
				changes: [{ service: 'applications.web', action: 'update' }],
				values: [{ name: 'TOKEN', action: 'update' }]
			})
		)
	).toBe('from staging · web updated · 1 value changed');
	expect(
		deploymentSummary(
			run('r', 'rollback', {
				revision: '3f9a1c2d4e5f',
				changes: [{ service: 'applications.web', action: 'update' }]
			})
		)
	).toBe('to 3f9a1c2d · web updated');
	expect(deploymentSummary(run('s', 'restart', { restarts: ['applications.web'] }))).toBe(
		'web restarted'
	);
	expect(
		deploymentSummary(
			run('f', 'deployment', {
				revision: 'abc',
				restarts: ['applications.a', 'applications.b', 'applications.c', 'applications.d']
			})
		)
	).toBe('a, b and 2 more restarted');
});

test('deploymentSummary reads an empty plan as no changes and a bare run as nothing', () => {
	expect(deploymentSummary(run('d', 'deployment', { revision: 'abc' }))).toBe('no changes');
	expect(deploymentSummary(run('b', 'backup'))).toBeNull();
	expect(deploymentSummary(run('o', 'deployment'))).toBeNull();
});

test('deploymentSummary keeps actions it does not know, after the known ones', () => {
	expect(
		deploymentSummary(
			run('d', 'deployment', {
				changes: [
					{ service: 'queues.jobs', action: 'replace' },
					{ service: 'applications.web', action: 'create' }
				]
			})
		)
	).toBe('web created · queues.jobs replace');
});

test('serviceChanges keeps the runs that changed one service, with what they did to it', () => {
	const runs = [
		run('restart-all', 'restart', { restarts: ['applications.api', 'applications.web'] }),
		run('restart-api', 'restart', { restarts: ['applications.api'] }),
		run('forced', 'deployment', {
			changes: [{ service: 'applications.web', action: 'update', detail: 'configuration changed' }],
			restarts: ['applications.web']
		}),
		run('values', 'deployment', { values: [{ name: 'TOKEN', action: 'update' }] }),
		run('backup', 'backup'),
		run('first', 'deployment', {
			changes: [
				{ service: 'applications.web', action: 'create' },
				{ service: 'databases.web', action: 'create' }
			]
		})
	];
	expect(
		serviceChanges(runs, 'applications.web').map((c) => [c.run.id, c.action, c.detail])
	).toEqual([
		['restart-all', 'restart', undefined],
		['forced', 'update', 'configuration changed; restarted'],
		['first', 'create', undefined]
	]);
	expect(serviceChanges(runs, 'databases.web').map((c) => c.run.id)).toEqual(['first']);
	expect(serviceChanges(runs, 'buckets.web')).toEqual([]);
});

test('serviceChanges keeps value changes that reach a service its plan rows leave out', () => {
	const runs = [
		run('values', 'deployment', {
			values: [
				{ name: 'TOKEN', action: 'update', services: ['applications.web', 'applications.api'] },
				{ name: 'SALT', action: 'update', services: ['applications.web'] }
			]
		}),
		run('both', 'deployment', {
			changes: [{ service: 'applications.web', action: 'update', detail: 'configuration changed' }],
			values: [{ name: 'TOKEN', action: 'update', services: ['applications.web'] }],
			restarts: ['applications.web']
		}),
		run('forced', 'deployment', {
			values: [{ name: 'TOKEN', action: 'update', services: ['applications.web'] }],
			restarts: ['applications.web']
		}),
		run('created', 'deployment', {
			changes: [{ service: 'applications.web', action: 'create' }],
			values: [{ name: 'TOKEN', action: 'create', services: ['applications.web'] }]
		})
	];
	expect(
		serviceChanges(runs, 'applications.web').map((c) => [c.run.id, c.action, c.detail])
	).toEqual([
		['values', 'update', 'values TOKEN, SALT changed'],
		['both', 'update', 'configuration changed; value TOKEN changed; restarted'],
		['forced', 'update', 'value TOKEN changed; restarted'],
		['created', 'create', undefined]
	]);
	expect(serviceChanges(runs, 'applications.api').map((c) => c.run.id)).toEqual(['values']);
});
