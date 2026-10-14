// The project change stream keeps every page of an open project current.
// The server only signals (a deploy started or finished, an environment was
// created, stopped, renamed, or deleted, the draft, values, or access
// changed); the Studio
// re-reads by re-running its loads, the same refresh a mutation does. Page
// data therefore follows changes made in another tab, by the CLI, or by
// the reconciler finishing a run, without any page polling for it.

import { goto, invalidateAll } from '$app/navigation';
import { resolve } from '$app/paths';
import { openStream, type StreamHandle } from '$lib/sse';
import { toast } from '$lib/stores/toast.svelte';
import type { Project } from '$lib/types/project';

// A burst of signals (a purge concludes its run and deletes its row back
// to back) costs one refresh.
const REFRESH_DEBOUNCE_MS = 250;

export function followProject(
	project: Pick<Project, 'id' | 'name' | 'display_name'>
): StreamHandle {
	let connected = false;
	let timer: ReturnType<typeof setTimeout> | null = null;
	const refresh = () => {
		if (timer) return;
		timer = setTimeout(() => {
			timer = null;
			void invalidateAll();
		}, REFRESH_DEBOUNCE_MS);
	};
	const handle = openStream<Record<string, never>>({
		path: `/v1/projects/${project.id}/stream`,
		events: ['ready', 'changed', 'deleted', 'revoked'],
		onEvent: (_data, _id, event) => {
			if (event === 'deleted') {
				handle.close();
				void leaveDeletedProject(project);
				return;
			}
			if (event === 'revoked') {
				handle.close();
				toast.info(`You no longer have access to ${project.display_name || project.name}`);
				void goto(resolve('/(app)/projects')).then(() => invalidateAll());
				return;
			}
			// The first ready only opens the stream; a later one is a
			// reconnect that may have missed changes.
			if (event === 'ready' && !connected) {
				connected = true;
				return;
			}
			refresh();
		}
	});
	return {
		close() {
			if (timer) clearTimeout(timer);
			handle.close();
		}
	};
}

// Both the delete dialog (a project without environments goes at once) and
// the stream (the last purge took the project) report a deletion; whichever
// is first announces it and leaves.
const announced = new Set<string>();

export async function leaveDeletedProject(project: Pick<Project, 'id' | 'name' | 'display_name'>) {
	if (announced.has(project.id)) return;
	announced.add(project.id);
	toast.success(`Project "${project.display_name || project.name}" deleted`);
	await goto(resolve('/(app)/projects'));
	await invalidateAll();
}
