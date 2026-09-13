import type { Project, ServiceHealth } from '$lib/types/project';

// Rollups over a project summary, shared by every listing of projects
// (cards, list rows) so the two never disagree.

export function projectServiceCount(project: Project): number {
	const counts = project.summary?.service_counts;
	return (counts?.applications ?? 0) + (counts?.databases ?? 0) + (counts?.buckets ?? 0);
}

// Worse health outranks better; unknown outranks healthy so a
// half-observed project never reads as fine.
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
