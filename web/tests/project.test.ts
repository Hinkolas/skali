import { expect, test } from 'vitest';
import {
	projectEnvironmentCount,
	projectHealthTitle,
	projectServiceCount,
	projectWorstHealth
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
		summary: { environments, service_counts: { applications: 2, databases: 1, buckets: 0 } }
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

test('environment counts fall back to the access map on the plain list', () => {
	expect(projectEnvironmentCount(project())).toBe(2);
	expect(projectEnvironmentCount(summary([env()]))).toBe(1);
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
