import type { Project, ServiceHealth } from '$lib/types/project';
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
