<script lang="ts">
	import { page } from '$app/state';
	import type { Node, Org, Project, Service } from '$lib/mock/types';
	import { toast } from '$lib/stores/toast.svelte';
	import SidebarOrgNav from './SidebarOrgNav.svelte';
	import SidebarProjectNav from './SidebarProjectNav.svelte';
	import SidebarServiceNav from './SidebarServiceNav.svelte';
	import SidebarStatus from './SidebarStatus.svelte';

	// Context-switching sidebar. The variant derives from merged `page.data`
	// keys set by nested layouts: `service` (+`project`) => service nav,
	// `project` => project nav, otherwise org nav. Breadcrumbs reads the same
	// contract; loads that introduce colliding `project`/`service` keys would
	// break both.
	const data = $derived(
		page.data as {
			org: Org;
			nodes: Node[];
			project?: Project;
			services?: Service[];
			service?: Service;
		}
	);

	const statusText = $derived.by(() => {
		const service = data.service;
		if (service) {
			if (service.type === 'application') {
				return `${service.instances} instance${service.instances === 1 ? '' : 's'} · ${service.instance_nodes}`;
			}
			return `on ${service.node} · healthy`;
		}
		const online = data.nodes.filter((n) => n.status === 'online').length;
		return `${online}/${data.nodes.length} nodes online`;
	});
</script>

<aside class="flex w-[275px] flex-none flex-col overflow-y-auto">
	<!-- Same height as the Topbar so both read as one aligned band. -->
	<!-- px-1 lines the logo box up with the nav pill edge (same inset as the
	     nav item containers), not the item icons. -->
	<div class="flex h-14 flex-none items-center gap-2.5 px-1">
		<div
			class="from-accent-from to-accent-to text-surface-base grid size-6.5 place-items-center rounded-lg bg-linear-135 text-lg font-bold"
		>
			s
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

	{#if data.service && data.project}
		<SidebarServiceNav project={data.project} service={data.service} />
	{:else if data.project}
		<SidebarProjectNav project={data.project} services={data.services ?? []} />
	{:else}
		<SidebarOrgNav org={data.org} />
	{/if}

	<div class="flex-1"></div>

	<SidebarStatus text={statusText} />
</aside>
