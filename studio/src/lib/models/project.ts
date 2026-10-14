import type { Project, ServiceHealth, SummaryEnvironment } from '$lib/types/project';
import { relativeTime } from '$lib/format';

// Rollups over a project summary, shared by every listing of projects
// (cards, list rows) so the two never disagree.

export function projectServiceCount(project: Project): number {
	const counts = project.summary?.service_counts;
	return (counts?.applications ?? 0) + (counts?.databases ?? 0) + (counts?.buckets ?? 0);
}

// Worse health outranks better; unknown outranks healthy so a
// half-observed project never reads as fine. The daemon ranks the same way
// in internal/module/health.go (WorstHealth).
const HEALTH_RANK: Record<ServiceHealth, number> = {
	healthy: 1,
	unknown: 2,
	progressing: 3,
	degraded: 4,
	unhealthy: 5
};

/** The worst environment health; locked environments carry none and stay
 * out of the rollup, and a project without environments is unknown. */
export function projectWorstHealth(project: Project): ServiceHealth {
	const environments = project.summary?.environments ?? [];
	return environments.reduce<ServiceHealth>(
		(acc, e) => (e.health && HEALTH_RANK[e.health] > HEALTH_RANK[acc] ? e.health : acc),
		environments.length ? 'healthy' : 'unknown'
	);
}

/** The summary's environments for a one-line rollup: high-priority ones
 * first, so the one a listing shows is never hidden behind its +N, then the
 * daemon's name order. Priority is configured, not read from the name. */
export function projectEnvironmentRollup(project: Project): SummaryEnvironment[] {
	const environments = project.summary?.environments ?? [];
	return environments.toSorted(
		(a, b) => Number(b.priority === 'high') - Number(a.priority === 'high')
	);
}

/** The project's last activity from the summary. Without one it falls back
 * to the settings time, which deploys never move. */
export function projectUpdatedAt(project: Project): string {
	return project.summary?.updated_at ?? project.updated_at;
}

export const PROJECT_SORTS = ['name-asc', 'name-desc', 'updated-desc', 'updated-asc'] as const;
export type ProjectSort = (typeof PROJECT_SORTS)[number];

/** The listing order: by the name a listing shows, or by the update time
 * (projectUpdatedAt). Ties fall back to the name so the order stays put
 * when two projects moved at the same instant. */
export function sortProjects(projects: Project[], sort: ProjectSort): Project[] {
	const label = (p: Project) => p.display_name || p.name;
	const byName = (a: Project, b: Project) =>
		label(a).localeCompare(label(b), undefined, { sensitivity: 'base', numeric: true }) ||
		a.name.localeCompare(b.name);
	const byUpdated = (a: Project, b: Project) =>
		Date.parse(projectUpdatedAt(a)) - Date.parse(projectUpdatedAt(b));
	switch (sort) {
		case 'name-asc':
			return projects.toSorted(byName);
		case 'name-desc':
			return projects.toSorted((a, b) => byName(b, a));
		case 'updated-desc':
			return projects.toSorted((a, b) => byUpdated(b, a) || byName(a, b));
		case 'updated-asc':
			return projects.toSorted((a, b) => byUpdated(a, b) || byName(a, b));
	}
}

/** Environments the caller can name: the summary when the list carried one,
 * else the access map, which lists every environment (locked ones as none).
 * The plain list therefore counts correctly on every route. */
export function projectEnvironmentCount(project: Project): number {
	return project.summary?.environments.length ?? Object.keys(project.access.environments).length;
}

/** Explains the health dot. The rollup is the kernel's cached verdict, so
 * the title says when it was evaluated, or that it was not yet (the first
 * pass after a daemon restart has not landed). Locked environments carry no
 * health and stay out of it. */
export function projectHealthTitle(project: Project): string {
	if (!project.summary) return 'health unavailable';
	const evaluated = project.summary.environments
		.filter((e) => e.access !== 'none')
		.map((e) => e.health_evaluated_at ?? null);
	if (evaluated.length === 0) return 'no environments to evaluate';
	if (evaluated.some((at) => at === null)) return 'health not evaluated yet';
	const oldest = (evaluated as string[]).reduce((a, b) => (Date.parse(b) < Date.parse(a) ? b : a));
	return `health evaluated ${relativeTime(oldest)}`;
}
