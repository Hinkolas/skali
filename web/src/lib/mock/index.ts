// Async accessor over the static mock graph dataset, shaped like a future
// API client so the graph page's load does not change when a real service
// graph lands. Everything else that used to live here is served by the real
// API now.

import { GRAPHS } from './data';
import type { ProjectGraph } from './types';

export type * from './types';

export async function getProjectGraph(projectSlug: string): Promise<ProjectGraph | null> {
	return GRAPHS[projectSlug] ?? null;
}
