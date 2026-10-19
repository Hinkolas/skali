import { expect, test } from 'vitest';
import { serviceProgress } from '../src/lib/models/runservices';
import type { Step } from '../src/lib/types/runs';

function step(key: string, status: Step['status'], extra: Partial<Step> = {}): Step {
	return {
		id: key,
		key,
		title: key,
		status,
		created_at: '2026-10-19T10:00:00Z',
		started_at: status === 'pending' ? null : '2026-10-19T10:00:00Z',
		finished_at: ['succeeded', 'failed', 'skipped', 'cancelled'].includes(status)
			? '2026-10-19T10:01:00Z'
			: null,
		attempts: [],
		...extra
	};
}

// A deployment as the server journals it: the opening steps, each
// application's artifacts, then the rollout with its per-service work.
function deployment(): Step[] {
	return [
		step('validate', 'succeeded'),
		step('artifacts', 'succeeded', {
			children: [
				step('artifacts.web', 'succeeded', {
					service: 'applications.web',
					children: [
						step('artifacts.web.build', 'succeeded', { service: 'applications.web' }),
						step('artifacts.web.verify', 'succeeded', { service: 'applications.web' })
					]
				})
			]
		}),
		step('rollout', 'running', {
			children: [
				step('apply:environment', 'succeeded'),
				step('claim:databases.main', 'succeeded', { service: 'databases.main' }),
				step('apply:worker', 'succeeded', { service: 'applications.worker' }),
				step('apply:web', 'succeeded', { service: 'applications.web' }),
				step('release:web', 'running', { service: 'applications.web' }),
				step('apply:buckets.assets', 'waiting', { service: 'buckets.assets' })
			]
		})
	];
}

test('serviceProgress is null for a run whose steps name no service', () => {
	expect(serviceProgress([step('verify', 'succeeded'), step('backup', 'running')])).toBeNull();
	expect(serviceProgress([])).toBeNull();
});

test('serviceProgress lists services by kind and key, the environment last', () => {
	const rows = serviceProgress(deployment())!;
	expect(rows.map((r) => [r.service, r.kind, r.name])).toEqual([
		['applications.web', 'application', 'web'],
		['applications.worker', 'application', 'worker'],
		['databases.main', 'database', 'main'],
		['buckets.assets', 'bucket', 'assets'],
		[null, null, 'environment']
	]);
});

test('serviceProgress rows hold the steps that do the work, in journal order', () => {
	const rows = serviceProgress(deployment())!;
	expect(rows[0].steps.map((s) => s.key)).toEqual([
		'artifacts.web.build',
		'artifacts.web.verify',
		'apply:web',
		'release:web'
	]);
	// The rollout and artifacts parents only group; the environment's own
	// work is what is left.
	expect(rows[4].steps.map((s) => s.key)).toEqual(['validate', 'apply:environment']);
});

test('serviceProgress names the step in flight and settles a row only once all of it has', () => {
	const rows = serviceProgress(deployment())!;
	const web = rows[0];
	expect(web.status).toBe('running');
	expect(web.current.key).toBe('release:web');
	expect(web.started_at).toBe('2026-10-19T10:00:00Z');
	expect(web.finished_at).toBeNull();

	const worker = rows[1];
	expect(worker.status).toBe('succeeded');
	expect(worker.current.key).toBe('apply:worker');
	expect(worker.finished_at).toBe('2026-10-19T10:01:00Z');

	expect(rows[3].status).toBe('waiting');
	// The rollout parent runs as long as the run does; the environment's
	// own steps are done.
	expect(rows[4].status).toBe('succeeded');
});

test('serviceProgress times a wait from its first observation', () => {
	// The journal stamps a step that waited as started when it completes;
	// it was created when the wait began.
	const provisioned = step('claim:databases.main', 'succeeded', {
		service: 'databases.main',
		created_at: '2026-10-19T10:00:00Z',
		started_at: '2026-10-19T10:05:00Z',
		finished_at: '2026-10-19T10:05:00Z'
	});
	const waiting = step('claim:databases.main', 'waiting', {
		service: 'databases.main',
		created_at: '2026-10-19T10:00:00Z',
		started_at: null
	});
	expect(serviceProgress([provisioned])![0]).toMatchObject({
		started_at: '2026-10-19T10:00:00Z',
		finished_at: '2026-10-19T10:05:00Z'
	});
	expect(serviceProgress([waiting])![0]).toMatchObject({
		started_at: '2026-10-19T10:00:00Z',
		finished_at: null
	});
	// Work not begun yet does not start the clock.
	const queued = serviceProgress([
		step('artifacts.web.build', 'pending', {
			service: 'applications.web',
			created_at: '2026-10-19T09:00:00Z'
		})
	])!;
	expect(queued[0].started_at).toBeNull();
});

test('serviceProgress shows a failure over work in flight and names the failed step', () => {
	const rows = serviceProgress([
		step('apply:web', 'succeeded', { service: 'applications.web' }),
		step('release:web', 'failed', { service: 'applications.web' }),
		step('tls:web-main', 'waiting', { service: 'applications.web' })
	])!;
	expect(rows).toHaveLength(1);
	expect(rows[0].status).toBe('failed');
	expect(rows[0].current.key).toBe('tls:web-main');

	const settled = serviceProgress([
		step('apply:web', 'succeeded', { service: 'applications.web' }),
		step('release:web', 'failed', { service: 'applications.web' })
	])!;
	expect(settled[0].current.key).toBe('release:web');
});

test('serviceProgress keeps an application in flight while its artifacts are', () => {
	// The build finished and the verify has not started: the artifacts
	// parent still runs.
	const rows = serviceProgress([
		step('artifacts', 'running', {
			children: [
				step('artifacts.web', 'running', {
					service: 'applications.web',
					children: [step('artifacts.web.build', 'succeeded', { service: 'applications.web' })]
				})
			]
		})
	])!;
	expect(rows[0].status).toBe('running');
	expect(rows[0].finished_at).toBeNull();
});

test('serviceProgress lets children inherit their parent service and keeps unknown kinds', () => {
	const rows = serviceProgress([
		step('artifacts.web', 'running', {
			service: 'applications.web',
			children: [step('artifacts.web.build', 'running')]
		}),
		step('apply:queues.jobs', 'succeeded', { service: 'queues.jobs' }),
		step('skipped', 'skipped', { service: 'applications.api' })
	])!;
	expect(rows.map((r) => [r.service, r.kind, r.name, r.status])).toEqual([
		['applications.api', 'application', 'api', 'skipped'],
		['applications.web', 'application', 'web', 'running'],
		['queues.jobs', null, 'queues.jobs', 'succeeded']
	]);
	expect(rows[1].steps.map((s) => s.key)).toEqual(['artifacts.web.build']);
});
