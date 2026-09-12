<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Rocket from '@lucide/svelte/icons/rocket';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import { page } from '$app/state';
	import { api, ApiError } from '$lib/api/client';
	import { requiredTitle, roleAtLeast } from '$lib/access';
	import type { Environment, Project } from '$lib/types/project';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { modal } from '$lib/stores/modal.svelte';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Menu from '$lib/components/ui/Menu.svelte';
	import MenuItem from '$lib/components/ui/MenuItem.svelte';
	import MenuSeparator from '$lib/components/ui/MenuSeparator.svelte';
	import PromoteModal, {
		modalOptions as promoteModalOptions
	} from '$lib/components/run/PromoteModal.svelte';
	import RedeployModal, {
		modalOptions as redeployModalOptions
	} from '$lib/components/run/RedeployModal.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';

	// Environment actions collected under one quiet topbar menu: everything
	// here acts on the whole environment the breadcrumb names, is used
	// rarely, and a menu leaves room for future entries (stop) without
	// growing the chrome. Service-scoped actions (restarting one service)
	// live on the service page instead. Deep eligibility (something
	// running, per-target refusals) belongs to the modals and the server;
	// items only gate what they can see cheaply.
	const data = $derived(
		page.data as {
			project?: Project;
			environments?: Environment[];
			env?: Environment | null;
		}
	);

	const env = $derived(data.env ?? null);
	const environments = $derived(data.environments ?? []);
	const mayDeploy = $derived(roleAtLeast(env?.access, 'deploy'));
	const deployTitle = $derived(
		mayDeploy ? undefined : requiredTitle('deploy', 'environment', env?.name ?? '')
	);
	const promoteTitle = $derived(
		environments.length < 2 ? 'no other environments to promote to' : deployTitle
	);

	function openPromote() {
		if (!env) return;
		modal.open(PromoteModal, { source: env, environments }, promoteModalOptions);
	}

	function openRedeploy() {
		if (!env) return;
		modal.open(RedeployModal, { env }, redeployModalOptions);
	}

	function confirmRestartAll() {
		const target = env;
		if (!target) return;
		dialog.confirm({
			title: `Restart every service in ${target.name}?`,
			description:
				'Recreates the pods of every application with the same revision, values, and images. ' +
				'Rollouts follow each deployment strategy; databases, buckets, and stored data are untouched.',
			confirmLabel: 'Restart all',
			onConfirm: async () => {
				try {
					const res = await api.post<{ run_id: string }>(`/v1/environments/${target.id}/restart`);
					toast.success(`Restarting ${target.name}`);
					sidepanel.open(RunDetailPanel, { runId: res.run_id }, { label: 'Run details' });
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not start the restart');
				}
			}
		});
	}
</script>

{#if data.project && env && env.access !== 'none'}
	<Menu
		label="Environment actions"
		align="end"
		panelClass="min-w-44"
		triggerClass="border-border-strong text-text-secondary bg-white/2 flex h-8 cursor-pointer items-center gap-2 rounded-[11px] border px-2.5 text-base font-medium transition-colors hover:bg-white/5 sm:px-3.5"
	>
		{#snippet trigger({ open })}
			<!-- Phone width shows the glyph alone; the label stays for assistive tech. -->
			<Ellipsis size={15} class="flex-none sm:hidden" />
			<span class="sr-only sm:not-sr-only">Actions</span>
			<ChevronDown
				size={13}
				class="text-text-ghost hidden flex-none transition-transform sm:block {open
					? 'rotate-180'
					: ''}"
			/>
		{/snippet}
		<MenuItem icon={Rocket} disabled={!!promoteTitle} title={promoteTitle} onselect={openPromote}>
			Promote
		</MenuItem>
		<MenuItem icon={RefreshCw} disabled={!!deployTitle} title={deployTitle} onselect={openRedeploy}>
			Redeploy
		</MenuItem>
		<MenuSeparator />
		<MenuItem
			icon={RotateCw}
			disabled={!!deployTitle}
			title={deployTitle}
			onselect={confirmRestartAll}
		>
			Restart all
		</MenuItem>
	</Menu>
{/if}
