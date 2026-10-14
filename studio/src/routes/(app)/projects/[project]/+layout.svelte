<script lang="ts">
	// Drives the live status store for the resolved environment. $effect runs
	// in the browser only; sync() is idempotent per environment id, so load
	// re-runs do not churn the stream. The stream's first event is the full
	// status document, so the layout paints before any status arrives and
	// consumers show a pending state until then.
	import { onDestroy, untrack, type Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import { followProject } from '$lib/projectstream';
	import LockedEnvironment from '$lib/components/access/LockedEnvironment.svelte';
	import EnvironmentDeleting from '$lib/components/project/EnvironmentDeleting.svelte';
	import ProjectDeleting from '$lib/components/project/ProjectDeleting.svelte';
	import type { LayoutData } from './$types';

	let { data, children }: { data: LayoutData; children: Snippet } = $props();

	// A locked environment has no readable contents: the operational pages
	// show the lock instead; the settings pages stay (project-level controls
	// and the environment list are still the caller's to see).
	const locked = $derived(data.env?.access === 'none' && !page.url.pathname.includes('/settings'));
	// A deleting environment's pages are its delete's progress instead. The
	// project-level settings (general, members) stay reachable.
	const projectSettings = $derived(/\/settings(\/members)?\/?$/.test(page.url.pathname));
	const deleting = $derived(data.env?.state === 'releasing' && !projectSettings);

	$effect(() => {
		if (data.env && data.env.access !== 'none' && data.env.state !== 'releasing') {
			envStatus.sync(data.env.id);
		} else envStatus.stop();
	});
	onDestroy(() => envStatus.stop());

	// One change stream per open project keeps every page below current.
	// Keyed on the id alone: each refresh hands in a new project object.
	const projectId = $derived(data.project.id);
	$effect(() => {
		void projectId;
		const handle = untrack(() => followProject(data.project));
		return () => handle.close();
	});

	// The environment last seen being deleted, so its disappearance reads as
	// the delete finishing rather than as a bad link.
	let lastDeleting: string | null = null;
	$effect(() => {
		if (data.env?.state === 'releasing') lastDeleting = data.env.name;
	});
	// A refresh landing before the rewrite would report the same miss twice.
	let reported: string | null = null;
	$effect(() => {
		const missing = data.missingEnv;
		if (missing === reported) return;
		reported = missing;
		if (!missing) return;
		untrack(() => {
			const fallback = data.env?.name ?? null;
			if (missing === lastDeleting) toast.success(`Environment ${missing} deleted`);
			else
				toast.info(
					fallback
						? `No environment named ${missing}; showing ${fallback}`
						: `No environment named ${missing}`
				);
			const url = new URL(page.url);
			if (fallback) url.searchParams.set('env', fallback);
			else url.searchParams.delete('env');
			// eslint-disable-next-line svelte/no-navigation-without-resolve -- same page, only ?env= rewritten
			void goto(url, { replaceState: true, noScroll: true, keepFocus: true });
		});
	});
</script>

{#if data.project.deleting_at}
	<ProjectDeleting project={data.project} environments={data.environments} />
{:else if locked && data.env}
	<LockedEnvironment environment={data.env} project={data.project} />
{:else if deleting && data.env}
	<EnvironmentDeleting
		environment={data.env}
		environments={data.environments}
		project={data.project}
	/>
{:else}
	{@render children()}
{/if}
