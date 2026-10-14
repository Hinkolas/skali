import { expect, test } from 'vitest';
import {
	projectEnvironmentCount,
	projectEnvironmentRollup,
	projectHealthTitle,
	projectServiceCount,
	projectUpdatedAt,
	projectWorstHealth,
	sortProjects,
	type ProjectSort
} from '../src/lib/models/project';
import type { Project, SummaryEnvironment } from '../src/lib/types/project';

function project(overrides: Partial<Project> = {}): Project {
	return {
		id: 'p1',
		name: 'shop',
		display_name: 'Shop',
		source_mode: 'managed',
		created_at: '2026-09-01T00:00:00Z',
		updated_at: '2026-09-01T00:00:00Z',
		access: { role: 'read', member: true, environments: { production: 'none', staging: 'read' } },
		...overrides
	};
}

function env(overrides: Partial<SummaryEnvironment> = {}): SummaryEnvironment {
	return { id: 'e1', name: 'production', access: 'read', state: 'active', ...overrides };
}

function summary(environments: SummaryEnvironment[]): Project {
	return project({
		summary: {
			environments,
			service_counts: { applications: 2, databases: 1, buckets: 0 },
			updated_at: '2026-10-11T12:00:00Z'
		}
	});
}

test('the worst environment health wins; unknown outranks healthy', () => {
	expect(projectWorstHealth(project())).toBe('unknown');
	expect(projectWorstHealth(summary([]))).toBe('unknown');
	expect(projectWorstHealth(summary([env({ health: 'healthy' })]))).toBe('healthy');
	expect(
		projectWorstHealth(summary([env({ health: 'healthy' }), env({ id: 'e2', health: 'degraded' })]))
	).toBe('degraded');
	expect(
		projectWorstHealth(summary([env({ health: 'healthy' }), env({ id: 'e2', health: 'unknown' })]))
	).toBe('unknown');
	// A locked environment carries no health and stays out of the rollup.
	expect(
		projectWorstHealth(summary([env({ health: 'healthy' }), env({ id: 'e2', access: 'none' })]))
	).toBe('healthy');
});

test('service counts sum the summary and read zero without one', () => {
	expect(projectServiceCount(project())).toBe(0);
	expect(projectServiceCount(summary([]))).toBe(3);
});

test('the update time is the summary activity, else the settings time', () => {
	expect(projectUpdatedAt(project())).toBe('2026-09-01T00:00:00Z');
	expect(projectUpdatedAt(summary([]))).toBe('2026-10-11T12:00:00Z');
});

test('projects sort by shown name or by update time, ties by name', () => {
	const at = (updated_at: string) => ({
		environments: [],
		service_counts: { applications: 0, databases: 0, buckets: 0 },
		updated_at
	});
	const a = project({
		id: 'a',
		name: 'alpha',
		display_name: '',
		summary: at('2026-10-01T00:00:00Z')
	});
	const b = project({
		id: 'b',
		name: 'zulu',
		display_name: 'Bravo',
		summary: at('2026-10-12T00:00:00Z')
	});
	const c = project({
		id: 'c',
		name: 'charlie',
		display_name: '',
		summary: at('2026-10-12T00:00:00Z')
	});
	const ids = (sort: ProjectSort) => sortProjects([c, a, b], sort).map((p) => p.id);

	expect(ids('name-asc')).toEqual(['a', 'b', 'c']);
	expect(ids('name-desc')).toEqual(['c', 'b', 'a']);
	expect(ids('updated-desc')).toEqual(['b', 'c', 'a']);
	expect(ids('updated-asc')).toEqual(['a', 'b', 'c']);
});

test('environment counts fall back to the access map on the plain list', () => {
	expect(projectEnvironmentCount(project())).toBe(2);
	expect(projectEnvironmentCount(summary([env()]))).toBe(1);
});

test('the environment rollup leads with high priority, not with a name', () => {
	expect(projectEnvironmentRollup(project())).toEqual([]);
	const names = (environments: SummaryEnvironment[]) =>
		projectEnvironmentRollup(summary(environments)).map((e) => e.name);
	// Normal priority keeps the daemon's name order, production included.
	expect(
		names([env({ name: 'development' }), env({ name: 'production', priority: 'normal' })])
	).toEqual(['development', 'production']);
	expect(
		names([
			env({ name: 'development' }),
			env({ name: 'preview', priority: 'high' }),
			env({ name: 'production', priority: 'normal' }),
			env({ name: 'staging', priority: 'high' }),
			env({ name: 'locked', access: 'none' })
		])
	).toEqual(['preview', 'staging', 'development', 'production', 'locked']);
});

test('the health title explains what the cached verdict rests on', () => {
	expect(projectHealthTitle(project())).toBe('health unavailable');
	expect(projectHealthTitle(summary([env({ access: 'none' })]))).toBe(
		'no environments to evaluate'
	);
	expect(projectHealthTitle(summary([env({ health: 'unknown' })]))).toBe(
		'health not evaluated yet'
	);
	expect(
		projectHealthTitle(
			summary([
				env({ health: 'healthy', health_evaluated_at: new Date(Date.now() - 5_000).toISOString() }),
				env({ id: 'e2', health: 'healthy', health_evaluated_at: new Date().toISOString() })
			])
		)
	).toBe('health evaluated 5s ago');
});
