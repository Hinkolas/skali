// The project change stream keeps every page of an open project current.
// The server only signals (a deploy started or finished, an environment was
// created, stopped, renamed, or deleted, the draft, values, or access
// changed); the Studio
// re-reads by re-running its loads, the same refresh a mutation does. Page
// data therefore follows changes made in another tab, by the CLI, or by
// the reconciler finishing a run, without any page polling for it.

import { goto, invalidateAll } from '$app/navigation';
import { resolve } from '$app/paths';
import { api, ApiError } from '$lib/api/client';
import { openStream, type StreamHandle } from '$lib/sse';
import { toast } from '$lib/stores/toast.svelte';
import type { Project } from '$lib/types/project';

// A burst of signals (a purge concludes its run and deletes its row back
// to back) costs one refresh.
const REFRESH_DEBOUNCE_MS = 250;

type ProjectRef = Pick<Project, 'id' | 'name' | 'display_name'>;

export function followProject(project: ProjectRef): StreamHandle {
	let closed = false;
	let checking = false;
	let timer: ReturnType<typeof setTimeout> | null = null;
	const refresh = () => {
		if (timer) return;
		timer = setTimeout(() => {
			timer = null;
			void invalidateAll();
		}, REFRESH_DEBOUNCE_MS);
	};
	const close = () => {
		closed = true;
		if (timer) clearTimeout(timer);
		timer = null;
		handle.close();
	};
	// A project deleted, or access lost, while the stream was disconnected
	// never reaches it as an event: the reconnect is refused with a 404
	// instead, which EventSource does not show. A failed connection
	// therefore asks whether the project is still there. Anything but a
	// 404 (the daemon restarting, the network) keeps the stream retrying;
	// the ready of its reconnect re-reads.
	const recheck = async () => {
		if (checking || closed) return;
		checking = true;
		try {
			await api.get(`/v1/projects/${project.id}`);
		} catch (err) {
			if (!closed && err instanceof ApiError && err.status === 404) {
				close();
				toast.info(`Project "${label(project)}" was deleted, or you no longer have access to it`);
				await leaveProject();
			}
		} finally {
			checking = false;
		}
	};
	const handle = openStream<Record<string, never>>({
		path: `/v1/projects/${project.id}/stream`,
		events: ['ready', 'changed', 'deleted', 'revoked'],
		onEvent: (_data, _id, event) => {
			if (event === 'deleted') {
				close();
				void leaveDeletedProject(project);
				return;
			}
			if (event === 'revoked') {
				close();
				toast.info(`You no longer have access to ${label(project)}`);
				void leaveProject();
				return;
			}
			// Every ready re-reads: there is no cursor to replay from, so the
			// first one covers what changed between the page's load and the
			// subscription, and a later one what a disconnect missed.
			refresh();
		},
		onFailure: () => void recheck()
	});
	return { close };
}

function label(project: ProjectRef) {
	return project.display_name || project.name;
}

async function leaveProject() {
	await goto(resolve('/(app)/projects'));
	await invalidateAll();
}

// Both the delete dialog (a project without environments goes at once) and
// the stream (the last purge took the project) report a deletion; whichever
// is first announces it and leaves.
const announced = new Set<string>();

export async function leaveDeletedProject(project: ProjectRef) {
	if (announced.has(project.id)) return;
	announced.add(project.id);
	toast.success(`Project "${label(project)}" deleted`);
	await leaveProject();
}
