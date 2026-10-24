import { expect, test } from 'vitest';
import {
	cpuCapacity,
	kubernetesLabel,
	nodeCharts,
	nodeSeries,
	nodeStats,
	nodeStatus
} from '../src/lib/models/nodes';
import { failingNow, type BackupTargetSummary } from '../src/lib/types/backups';
import type { NodeMetrics, NodeStorage } from '../src/lib/types/metrics';
import type { ClusterNode } from '../src/lib/types/nodes';

const GIB = 1024 ** 3;

function node(overrides: Partial<ClusterNode> = {}): ClusterNode {
	return {
		name: 'skali-app-04',
		role: 'agent',
		capabilities: [],
		ready: true,
		schedulable: true,
		last_heartbeat: null,
		...overrides
	};
}

const metrics: NodeMetrics = {
	window: '24h',
	step_seconds: 900,
	timestamps: ['2026-10-24T10:00:00Z', '2026-10-24T10:15:00Z', '2026-10-24T10:30:00Z'],
	nodes: [
		{
			name: 'skali-app-04',
			cpu_allocatable_millicores: 4000,
			memory_allocatable_bytes: 8 * GIB,
			cpu_millicores: [1000, 3000, null],
			memory_bytes: [2 * GIB, null, 4 * GIB]
		}
	]
};

const storage: NodeStorage = {
	name: 'skali-app-04',
	sampled_at: '2026-10-24T10:30:00Z',
	capacity_bytes: 80 * GIB,
	used_bytes: 20 * GIB,
	available_bytes: 60 * GIB,
	categories: {
		volumes_bytes: 0,
		databases_bytes: 0,
		objects_bytes: 0,
		images_bytes: 0,
		temporary_bytes: 0,
		system_bytes: 20 * GIB
	}
};

test('nodeStatus tells online, cordoned and offline apart', () => {
	expect(nodeStatus(node())).toEqual({ text: 'online', tone: 'success' });
	expect(nodeStatus(node({ schedulable: false }))).toEqual({ text: 'cordoned', tone: 'warning' });
	// Offline wins over cordoned: the node does not run at all.
	expect(nodeStatus(node({ ready: false, schedulable: false }))).toEqual({
		text: 'offline',
		tone: 'neutral'
	});
});

test('kubernetesLabel names the distribution before the version', () => {
	expect(kubernetesLabel('v1.36.3+k3s1')).toBe('k3s v1.36.3');
	expect(kubernetesLabel('v1.36.3')).toBe('v1.36.3');
	expect(kubernetesLabel('v1.36.3+1')).toBe('v1.36.3');
});

test('cpuCapacity counts cores', () => {
	expect(cpuCapacity(500)).toBe('of 500m');
	expect(cpuCapacity(1000)).toBe('of 1 core');
	expect(cpuCapacity(4000)).toBe('of 4 cores');
	expect(cpuCapacity(1500)).toBe('of 1.5 cores');
	expect(cpuCapacity(16000)).toBe('of 16 cores');
});

test('nodeStats reads the newest sample against what the node can allocate', () => {
	const [cpu, memory, disk] = nodeStats(metrics, nodeSeries(metrics, 'skali-app-04'), storage);
	// The newest CPU bucket is a gap: the one before it counts.
	expect(cpu).toMatchObject({ value: '75', unit: '% of 4 cores' });
	expect(cpu.split).toEqual([
		{ label: 'avg', value: '50%' },
		{ label: 'peak', value: '75%' }
	]);
	expect(memory).toMatchObject({ value: '50', unit: '% of 8.0 GiB' });
	expect(disk).toMatchObject({ value: '25', unit: '% of 80 GiB', progress: { pct: 25 } });
});

test('nodeStats says so before anything was sampled', () => {
	const stats = nodeStats(null, nodeSeries(null, 'skali-app-04'), null);
	expect(stats.map((s) => s.value)).toEqual(['n/a', 'n/a', 'n/a']);
});

test('nodeCharts draw up to the allocatable amount', () => {
	const charts = nodeCharts(metrics, metrics.nodes[0]);
	expect(charts.map((c) => [c.id, c.yDomain])).toEqual([
		['cpu', [0, 4000]],
		['memory', [0, 8 * GIB]]
	]);
});

function summary(runs: Partial<BackupTargetSummary['runs']>): BackupTargetSummary {
	return {
		target: {
			name: 'default',
			endpoint: 'https://s3.example.com',
			region: '',
			bucket: 'skali-backups',
			prefix: '',
			access_key_id: 'AKIA',
			created_at: '2026-10-01T00:00:00Z',
			updated_at: '2026-10-01T00:00:00Z'
		},
		reachable: true,
		checked_at: '2026-10-24T10:00:00Z',
		snapshots: null,
		runs: {
			since: '2026-10-17T10:00:00Z',
			succeeded: 0,
			failed: 0,
			backing_up: 0,
			restoring: 0,
			last_succeeded: null,
			last_failed: null,
			...runs
		},
		schedules: { environments: 0, scheduled: 0 }
	};
}

test('failingNow is a failure newer than the newest success', () => {
	const run = (finished_at: string) => ({ project: 'shop', environment: 'prod', finished_at });
	expect(failingNow(summary({}))).toBe(false);
	expect(failingNow(summary({ last_failed: run('2026-10-20T00:00:00Z') }))).toBe(true);
	expect(
		failingNow(
			summary({
				last_failed: run('2026-10-20T00:00:00Z'),
				last_succeeded: run('2026-10-21T00:00:00Z')
			})
		)
	).toBe(false);
});
