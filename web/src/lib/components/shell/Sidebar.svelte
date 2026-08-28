<script lang="ts">
	import LogoMark from '$lib/components/ui/LogoMark.svelte';
	import { page } from '$app/state';
	import type { OrgView } from '$lib/models/org';
	import type { ServiceView } from '$lib/models/service';
	import type { ClusterNode } from '$lib/types/nodes';
	import type { Project } from '$lib/types/project';
	import { toast } from '$lib/stores/toast.svelte';
	import SidebarOrgNav from './SidebarOrgNav.svelte';
	import SidebarProjectNav from './SidebarProjectNav.svelte';
	import SidebarStatus from './SidebarStatus.svelte';

	// Context-switching sidebar. The variant derives from merged `page.data`
	// keys set by nested layouts: `project` => project nav, otherwise org nav.
	// Service pages keep the project nav; service-level navigation lives in
	// the tab bar inside the content card (ServiceTabs). Breadcrumbs reads the
	// same contract; loads that introduce colliding keys would break both.
	const data = $derived(
		page.data as {
			org: OrgView;
			nodes: ClusterNode[];
			project?: Project;
			services?: ServiceView[];
		}
	);

	const online = $derived(data.nodes.filter((n) => n.ready).length);
	const statusText = $derived(
		data.nodes.length === 0 ? 'cluster not observed' : `${online}/${data.nodes.length} nodes online`
	);
	const statusOk = $derived(data.nodes.length > 0 && online === data.nodes.length);
</script>

<aside class="flex w-[275px] flex-none flex-col overflow-y-auto">
	<!-- Same height as the Topbar so both read as one aligned band. -->
	<!-- px-1 lines the logo box up with the nav pill edge (same inset as the
	     nav item containers), not the item icons. -->
	<div class="flex h-14 flex-none items-center gap-2.5 px-1">
		<div
			class="from-accent-from to-accent-to text-surface-base grid size-6.5 place-items-center rounded-lg bg-linear-135"
		>
			<LogoMark class="size-4" />
		</div>
		<div class="text-xl font-semibold tracking-[-0.01em]">skali</div>
		<button
			type="button"
			onclick={() => toast.info('Collapsing the sidebar is coming soon')}
			class="text-text-ghost hover:text-text-secondary ml-auto cursor-pointer px-1 text-base transition-colors"
			aria-label="Collapse sidebar"
		>
			«
		</button>
	</div>

	{#if data.project}
		<SidebarProjectNav project={data.project} services={data.services ?? []} />
	{:else}
		<SidebarOrgNav />
	{/if}

	<div class="flex-1"></div>

	<SidebarStatus text={statusText} ok={statusOk} />
</aside>
