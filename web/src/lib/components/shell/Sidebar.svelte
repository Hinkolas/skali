<script lang="ts">
	import { page } from '$app/state';
	import type { NodeInfo, Org, Project, Service } from '$lib/mock/types';
	import { toast } from '$lib/stores/toast.svelte';
	import SidebarOrgNav from './SidebarOrgNav.svelte';
	import SidebarProjectNav from './SidebarProjectNav.svelte';
	import SidebarServiceNav from './SidebarServiceNav.svelte';
	import SidebarStatus from './SidebarStatus.svelte';
	import UserCard from './UserCard.svelte';

	// Context-switching sidebar. The variant derives from merged `page.data`
	// keys set by nested layouts: `service` (+`project`) => service nav,
	// `project` => project nav, otherwise org nav. Loads that introduce
	// colliding `project`/`service` keys would break this contract.
	const data = $derived(
		page.data as {
			org: Org;
			nodes: NodeInfo[];
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
		const healthy = data.nodes.filter((n) => n.state === 'healthy').length;
		return `${healthy}/${data.nodes.length} nodes healthy`;
	});
</script>

<aside
	class="bg-surface-raised border-border-default flex w-[250px] flex-none flex-col overflow-y-auto rounded-2xl border"
>
	<div class="flex items-center gap-2.5 p-4 pb-3">
		<div
			class="from-accent-from to-accent-to text-surface-base grid size-6.5 place-items-center rounded-lg bg-linear-135 text-[14px] font-bold"
		>
			s
		</div>
		<div class="text-[15.5px] font-semibold tracking-[-0.01em]">skali</div>
		<button
			type="button"
			onclick={() => toast.info('Collapsing the sidebar is coming soon')}
			class="text-text-ghost hover:text-text-secondary ml-auto cursor-pointer px-1 text-[13px] transition-colors"
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
	<UserCard />
</aside>
