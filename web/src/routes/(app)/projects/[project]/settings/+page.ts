import { apiFetch } from '$lib/api/client';
import type { RevisionSummary, Target } from '$lib/types/revisions';
import type { Run } from '$lib/types/runs';
import type { EnvironmentStatus } from '$lib/types/status';
import type { PageLoad } from './$types';

/** What the environments list shows per row beyond the environment itself. */
export interface EnvironmentInsight {
	status: EnvironmentStatus | null;
	/** The newest deployment or rollback, whatever its outcome. */
	lastDeploy: Run | null;
}

// The current environment's revisions and target for the rollback list,
// plus one status and one run feed per readable environment for the
// environments card. A locked environment gets no requests: its contents
// are refused anyway.
export const load: PageLoad = async ({ parent, fetch }) => {
	const { env, environments } = await parent();

	const insightEntries = await Promise.all(
		environments
			.filter((e) => e.access !== 'none')
			.map(async (e): Promise<[string, EnvironmentInsight]> => {
				const [statusRes, runsRes] = await Promise.all([
					apiFetch(fetch, `/v1/environments/${e.id}/status`),
					apiFetch(fetch, `/v1/environments/${e.id}/runs`)
				]);
				const runs = runsRes.ok ? ((await runsRes.json()) as { runs: Run[] }).runs : [];
				return [
					e.id,
					{
						status: statusRes.ok ? ((await statusRes.json()) as EnvironmentStatus) : null,
						lastDeploy: runs.find((r) => r.kind === 'deployment' || r.kind === 'rollback') ?? null
					}
				];
			})
	);
	const insights = Object.fromEntries(insightEntries);

	if (!env) return { revisions: [], target: null, insights };
	const [revisionsRes, targetRes] = await Promise.all([
		apiFetch(fetch, `/v1/environments/${env.id}/revisions`),
		apiFetch(fetch, `/v1/environments/${env.id}/target`)
	]);
	return {
		revisions: revisionsRes.ok
			? ((await revisionsRes.json()) as { revisions: RevisionSummary[] }).revisions
			: [],
		target: targetRes.ok ? ((await targetRes.json()) as { target: Target }).target : null,
		insights
	};
};
