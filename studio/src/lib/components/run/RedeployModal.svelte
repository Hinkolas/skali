<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Redeploy',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Redeploy re-runs the environment's own active revision with its current
	// values: the way saved value changes roll out. The server plans the
	// exact diff (which values move, which services roll); artifacts are
	// reused by construction, so the deployment is opened and completed right
	// here and the run continues on the server, followed in the run side
	// panel.
	import CheckCircle2 from '@lucide/svelte/icons/circle-check-big';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { api, ApiError } from '$lib/api/client';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';
	import PlanChanges from '$lib/components/run/PlanChanges.svelte';
	import type { Environment } from '$lib/types/project';
	import type { EnvironmentStatus } from '$lib/types/status';
	import type { OpenedDeployment, PlanResult } from '$lib/types/deploy';

	let {
		env,
		close
	}: {
		env: Environment;
		close: (redeployed?: boolean) => void;
	} = $props();

	let planning = $state(true);
	let planError = $state('');
	let planned = $state<PlanResult | null>(null);
	// undefined while loading, null when the environment runs nothing.
	let activeRev = $state<string | null | undefined>(undefined);
	let redeploying = $state(false);

	const short = (checksum: string) => checksum.slice(0, 8);

	// One status read for the blocked state and the revision line; the plan
	// request is the authority and refuses an empty environment anyway.
	// svelte-ignore state_referenced_locally
	void api
		.get<EnvironmentStatus>(`/v1/environments/${env.id}/status`)
		.then((s) => (activeRev = s.active_revision?.checksum ?? null))
		.catch(() => {});

	// svelte-ignore state_referenced_locally
	void api
		.post<PlanResult>(`/v1/environments/${env.id}/plan`, { redeploy: true })
		.then((result) => (planned = result))
		.catch((err) => {
			planError = err instanceof ApiError ? err.message : 'Could not plan the redeploy';
		})
		.finally(() => (planning = false));

	const ready = $derived(
		!planning && !redeploying && !planError && !!planned && !planned.up_to_date
	);

	async function redeploy() {
		if (!ready) return;
		redeploying = true;
		try {
			const opened = await api.post<OpenedDeployment>(`/v1/environments/${env.id}/deployments`, {
				redeploy: true
			});
			if (opened.up_to_date || !opened.deployment) {
				toast.success(`${env.name} is already up to date`);
				close(false);
				return;
			}
			// Every action is a reuse by construction; anything else is a
			// server bug and fails the window rather than guessing.
			const stray = (opened.actions ?? []).find((a) => a.action !== 'reuse');
			if (stray) {
				await api.post(`/v1/deployments/${opened.deployment.id}/fail`).catch(() => {});
				throw new ApiError(
					500,
					'internal',
					`unexpected ${stray.action} action for ${stray.application} in a redeploy`
				);
			}
			await api.post(`/v1/deployments/${opened.deployment.id}/complete`);
			toast.success(`Redeploying ${env.name}`);
			const runId = opened.deployment.run_id;
			close(true);
			if (runId) sidepanel.open(RunDetailPanel, { runId }, { label: 'Run details' });
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not start the redeploy');
		} finally {
			redeploying = false;
		}
	}
</script>

{#if activeRev === null}
	<ModalHeader title={env.name} mono>
		Re-run the revision deployed here with the current values.
	</ModalHeader>
	<div class="flex flex-col items-center gap-1.5 px-5.5 py-9 text-center">
		<RefreshCw size={18} class="text-text-ghost" />
		<p class="text-text-primary text-base font-medium">
			Nothing is running in {env.name} yet.
		</p>
		<p class="text-text-muted text-md">
			A redeploy re-runs the deployed revision; deploy into {env.name} first.
		</p>
	</div>
	<div
		class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3"
	>
		<Button variant="secondary" onclick={() => close(false)}>Close</Button>
	</div>
{:else}
	<ModalHeader
		title="Redeploy {env.name}"
		description="The deployed revision re-runs with the environment's current values; artifacts are reused, nothing rebuilds."
	/>

	<div class="flex flex-col gap-3.5 px-5.5 py-4">
		{#if activeRev}
			<span class="text-text-muted text-md">
				Revision <span class="font-mono text-text-secondary">{short(activeRev)}</span> is running.
			</span>
		{/if}

		{#if planning}
			<div class="text-text-muted flex items-center gap-2 text-md">
				<LoaderCircle size={14} class="animate-spin flex-none" />
				planning the redeploy…
			</div>
		{:else if planError}
			<div class="text-status-danger text-md">{planError}</div>
		{:else if planned}
			{#each planned.warnings ?? [] as warning (warning.code)}
				<p role="status" class="text-status-warning text-sm">{warning.message}</p>
			{/each}
			{#if planned.up_to_date}
				<div class="flex items-center gap-2.5">
					<CheckCircle2 size={16} class="text-status-success flex-none" />
					<span class="text-text-muted text-md">
						Everything is up to date; there is nothing to apply.
					</span>
				</div>
			{:else}
				<PlanChanges
					changes={planned.plan?.changes ?? []}
					values={planned.plan?.values ?? []}
					class="max-h-60"
				/>
			{/if}
		{/if}
	</div>

	<div
		class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3"
	>
		<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
		<Button variant="primary" busy={redeploying} disabled={!ready} onclick={redeploy}>
			Redeploy
		</Button>
	</div>
{/if}
