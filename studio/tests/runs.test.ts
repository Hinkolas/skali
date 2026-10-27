import { expect, test } from 'vitest';
import {
	activeRun,
	followRun,
	isBuildStep,
	reportMayArrive,
	slowestBuildSteps,
	type BuildInsights,
	type Run
} from '../src/lib/types/runs';

function run(id: string, status: Run['status'], kind: Run['kind'] = 'deployment'): Run {
	return {
		id,
		kind,
		project_id: 'p1',
		environment_id: 'e1',
		actor: 'u1',
		status,
		created_at: '2026-10-19T00:00:00Z',
		started_at: null,
		finished_at: null
	};
}

test('activeRun prefers the running run over queued ones', () => {
	const runs = [run('queued', 'pending'), run('now', 'running', 'backup'), run('old', 'succeeded')];
	expect(activeRun(runs)?.id).toBe('now');
});

test('activeRun falls back to the newest queued run', () => {
	const runs = [run('newer', 'pending'), run('older', 'pending'), run('done', 'failed')];
	expect(activeRun(runs)?.id).toBe('newer');
});

test('activeRun is null when every run has settled', () => {
	expect(activeRun([run('a', 'succeeded'), run('b', 'cancelled')])).toBeNull();
	expect(activeRun([])).toBeNull();
});

test('followRun adopts the active run of the selected environment', () => {
	expect(followRun(null, 'e1', 'r1')).toEqual({ envId: 'e1', runId: 'r1' });
	expect(followRun({ envId: 'e1', runId: 'r1' }, 'e1', 'r2')).toEqual({ envId: 'e1', runId: 'r2' });
});

test('followRun keeps a settled run on screen in its environment', () => {
	const followed = { envId: 'e1', runId: 'r1' };
	expect(followRun(followed, 'e1', null)).toBe(followed);
	expect(followRun(followed, 'e1', 'r1')).toBe(followed);
});

test('followRun drops a run of another environment', () => {
	const followed = { envId: 'e1', runId: 'r1' };
	expect(followRun(followed, 'e2', null)).toBeNull();
	expect(followRun(followed, null, null)).toBeNull();
	expect(followRun(followed, 'e2', 'r9')).toEqual({ envId: 'e2', runId: 'r9' });
	// Back in the first environment, the old outcome does not come back.
	expect(followRun(followRun(followed, 'e2', null), 'e1', null)).toBeNull();
});

test('isBuildStep matches only an application build step', () => {
	expect(isBuildStep('artifacts.web.build')).toBe(true);
	expect(isBuildStep('artifacts.web')).toBe(false);
	expect(isBuildStep('artifacts.web.upload')).toBe(false);
	expect(isBuildStep('artifacts..build')).toBe(false);
	expect(isBuildStep('rollout.web.build')).toBe(false);
});

test('slowestBuildSteps orders the timed steps slowest first', () => {
	const insights: BuildInsights = {
		outcome: 'succeeded',
		engine: 'docker-buildx',
		platform: 'linux/arm64',
		started_at: '2026-10-27T09:55:25Z',
		finished_at: '2026-10-27T09:55:47Z',
		steps: [
			{ name: 'COPY package.json .', status: 'cached' },
			{ name: 'RUN bun install', status: 'done', seconds: 2.4 },
			{ name: 'RUN bun run build', status: 'done', seconds: 17.6 },
			{ name: 'exporting to image', status: 'done', seconds: 1.1 }
		],
		log: '',
		log_lines: 0,
		log_truncated: false
	};
	expect(slowestBuildSteps(insights, 2).map((step) => step.name)).toEqual([
		'RUN bun run build',
		'RUN bun install'
	]);
});

test('reportMayArrive waits only shortly after the build finished', () => {
	const finished = Date.parse('2026-10-27T09:55:47Z');
	expect(reportMayArrive('2026-10-27T09:55:47Z', finished + 5_000)).toBe(true);
	expect(reportMayArrive('2026-10-27T09:55:47Z', finished + 60_000)).toBe(false);
	expect(reportMayArrive(null, finished)).toBe(false);
});
