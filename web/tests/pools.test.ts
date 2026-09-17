import { expect, test } from 'vitest';
import {
	formatGiB,
	overridesEqual,
	parseGiB,
	poolPhase,
	restartRequired,
	type DatabasePool
} from '../src/lib/types/pools';

const MIB = 1024 * 1024;

function pool(overrides: Partial<DatabasePool> = {}): DatabasePool {
	return {
		name: 'pg17-shared',
		class: 'shared',
		engine: 'postgres',
		major: 17,
		instances: 2,
		storage_bytes: 100 * 1024 * MIB,
		image: 'ghcr.io/cloudnative-pg/postgresql:17.9-system-trixie',
		node_port: null,
		state: 'active',
		memory: { bytes: 3328 * MIB, auto: true, node: 'db-1' },
		parameters: {
			effective: { shared_buffers: '832MB', work_mem: '8MB' },
			overrides: {},
			restart_keys: ['max_connections', 'max_worker_processes', 'shared_buffers', 'wal_buffers'],
			allowed: ['shared_buffers', 'work_mem', 'max_connections']
		},
		observed: null,
		created_at: '2026-09-16T00:00:00Z',
		updated_at: '2026-09-16T00:00:00Z',
		...overrides
	};
}

test('GiB input parses to whole MiB and formats back without trailing zeros', () => {
	expect(parseGiB('4')).toBe(4096 * MIB);
	expect(parseGiB(' 3.25 ')).toBe(3328 * MIB);
	expect(parseGiB('0.001')).toBe(1 * MIB);
	for (const bad of ['', '0', '-1', '4Gi', '1e3', 'abc']) expect(parseGiB(bad)).toBeNull();

	expect(formatGiB(4096 * MIB)).toBe('4');
	expect(formatGiB(3328 * MIB)).toBe('3.25');
	expect(formatGiB(1536 * MIB)).toBe('1.5');
});

test('observed phase renders as a pill', () => {
	expect(poolPhase(pool())).toEqual({ text: 'not observed', tone: 'neutral' });
	expect(
		poolPhase(
			pool({ observed: { phase: 'Cluster in healthy state', instances: 2, ready_instances: 2 } })
		)
	).toEqual({ text: 'healthy · 2/2 ready', tone: 'success' });
	expect(
		poolPhase(pool({ observed: { phase: 'Upgrading cluster', instances: 2, ready_instances: 1 } }))
	).toEqual({ text: 'upgrading cluster · 1/2 ready', tone: 'warning' });
});

test('restart keys come from the budget and from changed restart overrides', () => {
	const p = pool({
		parameters: {
			effective: {},
			overrides: { max_connections: '200', work_mem: '64MB' },
			restart_keys: ['max_connections', 'shared_buffers'],
			allowed: []
		}
	});
	expect(restartRequired(p, false, { max_connections: '200', work_mem: '32MB' })).toEqual([]);
	expect(restartRequired(p, true, { max_connections: '200' })).toEqual(['shared_buffers']);
	expect(restartRequired(p, false, { max_connections: '300' })).toEqual(['max_connections']);
	expect(restartRequired(p, true, {})).toEqual(['max_connections', 'shared_buffers']);
});

test('override maps compare by key and value', () => {
	expect(overridesEqual({}, {})).toBe(true);
	expect(overridesEqual({ a: '1' }, { a: '1' })).toBe(true);
	expect(overridesEqual({ a: '1' }, { a: '2' })).toBe(false);
	expect(overridesEqual({ a: '1' }, {})).toBe(false);
});

import {
	PARAMETER_GROUPS,
	PARAMETER_META,
	databasePhase,
	groupedParameters,
	memberSummary,
	poolBudgetText,
	poolSubtitle,
	type DatabasePoolDetail,
	type PoolMetrics
} from '../src/lib/types/pools';
import { poolCharts, poolStats, stepLabel, transactionSeries } from '../src/lib/models/pools';

// The backend's pgtune.Allowed, kept in lockstep by hand.
const ALLOWED = [
	'checkpoint_completion_target',
	'default_statistics_target',
	'effective_cache_size',
	'effective_io_concurrency',
	'maintenance_work_mem',
	'max_connections',
	'max_parallel_workers',
	'max_parallel_workers_per_gather',
	'max_wal_size',
	'max_worker_processes',
	'min_wal_size',
	'random_page_cost',
	'shared_buffers',
	'wal_buffers',
	'wal_keep_size',
	'work_mem'
];

function detail(overrides: Partial<DatabasePoolDetail> = {}): DatabasePoolDetail {
	return {
		...pool({ parameters: { ...pool().parameters, allowed: ALLOWED } }),
		members: [],
		databases: [],
		...overrides
	};
}

function metrics(overrides: Partial<PoolMetrics> = {}): PoolMetrics {
	const timestamps = ['2026-09-17T10:00:00Z', '2026-09-17T10:15:00Z', '2026-09-17T10:30:00Z'];
	return {
		pool: 'pg17-shared',
		window: '24h',
		step_seconds: 900,
		timestamps,
		cpu_millicores: [100, null, 300],
		memory_bytes: [1000 * MIB, 1200 * MIB, 1400 * MIB],
		connections: [10, 20, 12],
		commits: [30, null, 50],
		rollbacks: [1, 2, null],
		blks_hit: [900, null, 700],
		blks_read: [100, null, 300],
		cache_hit_ratio: [0.9, null, 0.7],
		database_bytes: [5 * MIB, 6 * MIB, 7 * MIB],
		instances_ready: [2, 2, 2],
		current: {
			sampled_at: timestamps[2],
			cpu_millicores: 300,
			memory_bytes: 1400 * MIB,
			instances: 2,
			instances_ready: 2,
			connections: 12,
			database_bytes: 7 * MIB,
			memory_budget_bytes: 3328 * MIB,
			max_connections: 100
		},
		...overrides
	};
}

test('parameter metadata covers exactly the backend allowlist', () => {
	expect(Object.keys(PARAMETER_META).sort()).toEqual(ALLOWED);
	for (const meta of Object.values(PARAMETER_META)) {
		expect(PARAMETER_GROUPS.map((g) => g.id)).toContain(meta.group);
		expect(meta.blurb).not.toBe('');
	}
});

test('grouped parameters place every allowed key once, in group order', () => {
	const groups = groupedParameters(detail());
	expect(groups.map((g) => g.id)).toEqual(['memory', 'connections', 'planner', 'wal', 'workers']);
	const keys = groups.flatMap((g) => g.rows.map((r) => r.key));
	expect(keys.toSorted()).toEqual(ALLOWED);
	expect(groups[0].rows.map((r) => r.key)).toEqual([
		'shared_buffers',
		'effective_cache_size',
		'work_mem',
		'maintenance_work_mem',
		'wal_buffers'
	]);
	const sharedBuffers = groups[0].rows[0];
	expect(sharedBuffers.restart).toBe(true);
	expect(sharedBuffers.effective).toBe('832MB');
	expect(sharedBuffers.unit).toBe('memory');
	expect(groups[1].rows[0].effective).toBe('');

	// An allowed key this console does not know lands in Other; groups
	// without allowed keys vanish.
	const narrow = groupedParameters(
		detail({
			parameters: { ...pool().parameters, allowed: ['work_mem', 'brand_new_knob'] }
		})
	);
	expect(narrow.map((g) => g.id)).toEqual(['memory', 'other']);
	expect(narrow[1].rows[0]).toMatchObject({ key: 'brand_new_knob', blurb: '', unit: 'integer' });
});

test('header facts read as one line each', () => {
	expect(poolSubtitle(detail())).toBe('shared · postgres 17 · 2 instances · 100 GiB');
	expect(poolSubtitle(detail({ instances: 1 }))).toBe(
		'shared · postgres 17 · 1 instance · 100 GiB'
	);

	expect(poolBudgetText(detail())).toBe('3.3 GiB · automatic from db-1');
	expect(poolBudgetText(detail({ memory: { bytes: 3328 * MIB, auto: true, capped: true } }))).toBe(
		'3.3 GiB · automatic, capped'
	);
	expect(poolBudgetText(detail({ memory: { bytes: 4096 * MIB, auto: false } }))).toBe(
		'4.0 GiB · custom'
	);
	expect(poolBudgetText(detail({ memory: { bytes: null, auto: true } }))).toBe(
		'unknown · no database node observed yet'
	);

	const member = (role: 'primary' | 'replica' | '') => ({
		name: 'x',
		role,
		ready: true,
		restarts: 0,
		started_at: null,
		phase: 'Running'
	});
	expect(memberSummary([])).toBe('no instances observed');
	expect(memberSummary([member('primary'), member('replica'), member('replica')])).toBe(
		'1 primary · 2 replicas'
	);
	expect(memberSummary([member('primary'), member('')])).toBe('1 primary · 1 starting');

	expect(databasePhase('provisioned')).toEqual({ text: 'provisioned', tone: 'success' });
	expect(databasePhase('releasing')).toEqual({ text: 'releasing', tone: 'warning' });
	expect(databasePhase('pending')).toEqual({ text: 'pending', tone: 'neutral' });
});

test('stat tiles degrade without metrics and fill from the newest sample', () => {
	const empty = poolStats(null, detail());
	expect(empty.map((s) => s.label)).toEqual(['CONNECTIONS', 'CPU', 'MEMORY', 'STORAGE']);
	expect(empty[0]).toMatchObject({ value: 'n/a', note: 'no data yet' });
	expect(empty[1]).toMatchObject({ value: 'n/a', note: 'no data yet' });
	expect(empty[3]).toMatchObject({ value: '100 GiB', note: 'volume size, usage not yet measured' });

	const full = poolStats(metrics(), detail());
	expect(full[0]).toMatchObject({ value: '12', unit: '/ 100' });
	expect(full[0].sparkline).toHaveLength(3);
	expect(full[0].split).toEqual([
		{ label: 'avg', value: '14' },
		{ label: 'peak', value: '20' }
	]);
	expect(full[1]).toMatchObject({ value: '300', unit: 'mCPU' });
	expect(full[2]).toMatchObject({ value: '1.4', unit: 'GiB / 3.3 GiB' });
	expect(full[3]).toMatchObject({ value: '7.0', unit: 'MiB / 100 GiB' });
	expect(full[3].progress?.pct).toBeCloseTo((7 / (100 * 1024)) * 100);

	// Without a budget the memory tile says so; without a max in the sample
	// the effective parameters supply it; a cpu in cores switches unit.
	const sparse = poolStats(
		metrics({
			current: {
				...metrics().current!,
				cpu_millicores: 2500,
				memory_budget_bytes: null,
				max_connections: null,
				connections: 5
			}
		}),
		detail({
			memory: { bytes: null, auto: true },
			parameters: { ...pool().parameters, effective: { max_connections: '200' } }
		})
	);
	expect(sparse[0]).toMatchObject({ value: '5', unit: '/ 200' });
	expect(sparse[1]).toMatchObject({ value: '2.5', unit: 'cores' });
	expect(sparse[2]).toMatchObject({ unit: 'GiB', note: 'budget not sized yet' });
});

test('charts derive transactions and percentages with gaps kept', () => {
	expect(transactionSeries(metrics())).toEqual([31, 2, 50]);
	expect(stepLabel(60)).toBe('1min');
	expect(stepLabel(900)).toBe('15min');
	expect(stepLabel(3600)).toBe('1h');

	const charts = poolCharts(metrics());
	expect(charts.map((c) => c.id)).toEqual([
		'connections',
		'transactions',
		'cpu',
		'memory',
		'cache',
		'storage'
	]);
	expect(charts[1].suffix).toBe('/ 15min');
	expect(charts[1].series.map((s) => s.label)).toEqual(['commits', 'rollbacks']);
	const cache = charts[4];
	expect(cache.yDomain).toEqual([0, 100]);
	expect(cache.series[0].points.map((p) => p.v)).toEqual([90, null, 70]);
	expect(cache.format(99.96)).toBe('100%');
	expect(cache.format(87.25)).toBe('87.3%');
	expect(charts[2].format(2500)).toBe('2.50 cores');
	expect(charts[2].format(300)).toBe('300 mCPU');
});
