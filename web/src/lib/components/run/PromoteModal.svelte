<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Promote',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Promote flow started from the source environment: pick an eligible
	// target, review the server's plan, confirm. A promotion reuses artifacts
	// by construction, so the deployment is opened and completed right here
	// and the run continues on the server, followed in the run side panel.
	// The server stays the authority on eligibility; the list only explains
	// the refusals it can already see.
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import ChevronRight from '@lucide/svelte/icons/chevron-right';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import Lock from '@lucide/svelte/icons/lock';
	import { api, ApiError } from '$lib/api/client';
	import { requiredTitle, roleAtLeast } from '$lib/access';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';
	import type { Environment } from '$lib/types/project';
	import type { EnvironmentStatus } from '$lib/types/status';
	import type { OpenedDeployment, PlanResult } from '$lib/types/deploy';

	let {
		source,
		environments,
		close
	}: {
		source: Environment;
		/** All environments of the project, for the target list. */
		environments: Environment[];
		close: (promoted?: boolean) => void;
	} = $props();

	// svelte-ignore state_referenced_locally
	const candidates = environments.filter((e) => e.id !== source.id);

	// The reason a target is out of reach, or null when it may be picked.
	function refusal(t: Environment): string | null {
		if (t.access === 'none') return 'locked for you';
		if (!roleAtLeast(t.access, 'deploy')) return requiredTitle('deploy', 'environment', t.name);
		const s = t.settings;
		if (
			s?.deploy_policy === 'promote-only' &&
			s.promote_from.length &&
			!s.promote_from.includes(source.name)
		) {
			return `accepts promotions from ${s.promote_from.join(' or ')} only`;
		}
		return null;
	}

	let target = $state<Environment | null>(null);
	let planning = $state(false);
	let planError = $state('');
	let planned = $state<PlanResult | null>(null);
	// undefined while loading, null when the environment runs nothing.
	let sourceRev = $state<string | null | undefined>(undefined);
	let targetRev = $state<string | null | undefined>(undefined);
	let promoting = $state(false);

	const short = (checksum: string) => checksum.slice(0, 8);

	function pick(t: Environment) {
		target = t;
		planning = true;
		planError = '';
		planned = null;
		sourceRev = undefined;
		targetRev = undefined;
		// The revision line is decoration next to the plan; a failed status
		// read just leaves it out.
		void api
			.get<EnvironmentStatus>(`/v1/environments/${source.id}/status`)
			.then((s) => (sourceRev = s.active_revision?.checksum ?? null))
			.catch(() => (sourceRev = null));
		void api
			.get<EnvironmentStatus>(`/v1/environments/${t.id}/status`)
			.then((s) => (targetRev = s.active_revision?.checksum ?? null))
			.catch(() => (targetRev = null));
		void api
			.post<PlanResult>(`/v1/environments/${t.id}/plan`, { from_environment_id: source.id })
			.then((result) => (planned = result))
			.catch((err) => {
				planError = err instanceof ApiError ? err.message : 'Could not plan the promotion';
			})
			.finally(() => (planning = false));
	}

	function back() {
		target = null;
		planned = null;
		planError = '';
	}

	const ready = $derived(
		!!target && !planning && !promoting && !planError && !!planned && !planned.up_to_date
	);

	async function promote() {
		if (!ready || !target) return;
		promoting = true;
		try {
			const opened = await api.post<OpenedDeployment>(`/v1/environments/${target.id}/deployments`, {
				from_environment_id: source.id
			});
			if (opened.up_to_date || !opened.deployment) {
				toast.success(`${target.name} is already up to date`);
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
					`unexpected ${stray.action} action for ${stray.application} in a promotion`
				);
			}
			await api.post(`/v1/deployments/${opened.deployment.id}/complete`);
			toast.success(`Promoting ${source.name} to ${target.name}`);
			const runId = opened.deployment.run_id;
			close(true);
			if (runId) sidepanel.open(RunDetailPanel, { runId }, { label: 'Run details' });
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not start the promotion');
		} finally {
			promoting = false;
		}
	}

	function actionClass(action: string, destructive?: boolean): string {
		if (destructive || action === 'delete' || action === 'unset') return 'text-status-danger';
		if (action === 'create' || action === 'set') return 'text-status-success';
		return 'text-text-tertiary';
	}
</script>

<ModalHeader title={source.name} mono>
	Promote the revision running here to another environment.
</ModalHeader>

{#if !target}
	<div class="flex flex-col gap-2 overflow-y-auto px-5.5 py-4">
		<span class="text-text-primary text-base font-medium">Promote to</span>
		{#if candidates.length}
			<div class="border-border-subtle divide-border-subtle divide-y rounded-xl border">
				{#each candidates as t (t.id)}
					{@const reason = refusal(t)}
					<button
						type="button"
						disabled={!!reason}
						title={reason ?? undefined}
						onclick={() => pick(t)}
						class="flex min-h-11.5 w-full items-center gap-2 px-3 py-1.5 text-left transition-colors first:rounded-t-xl last:rounded-b-xl focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/70 {reason
							? 'cursor-default'
							: 'cursor-pointer hover:bg-white/4'}"
					>
						{#if t.access === 'none'}
							<Lock size={12} class="text-text-ghost flex-none" />
						{/if}
						<span class="font-mono text-md {reason ? 'text-text-ghost' : 'text-text-primary'}">
							{t.name}
						</span>
						{#if t.settings?.deploy_policy === 'promote-only'}
							<Pill text="protected" tone="warning" />
						{/if}
						<span class="ml-auto flex items-center">
							{#if reason}
								<span class="text-text-faint text-sm">{reason}</span>
							{:else}
								<ChevronRight size={14} class="text-text-ghost flex-none" />
							{/if}
						</span>
					</button>
				{/each}
			</div>
		{:else}
			<span class="text-text-muted text-md">No other environments in this project yet.</span>
		{/if}
	</div>

	<div
		class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3"
	>
		<Button variant="secondary" onclick={() => close(false)}>Cancel</Button>
	</div>
{:else}
	<div class="flex flex-col gap-3 overflow-y-auto px-5.5 py-4">
		<div class="flex items-center gap-2.5">
			<span class="font-mono text-text-secondary text-md">{source.name}</span>
			<ArrowRight size={13} class="text-text-ghost flex-none" />
			<span class="font-mono text-text-primary text-md">{target.name}</span>
			{#if target.settings?.deploy_policy === 'promote-only'}
				<Pill text="protected" tone="warning" />
			{/if}
		</div>

		{#if sourceRev}
			<span class="text-text-muted text-md">
				Revision <span class="font-mono text-text-secondary">{short(sourceRev)}</span>
				{#if targetRev}
					replaces <span class="font-mono text-text-secondary">{short(targetRev)}</span> in {target.name}.
				{:else if targetRev === null}
					is the first revision in {target.name}.
				{/if}
			</span>
		{/if}

		{#if planning}
			<div class="text-text-muted flex items-center gap-2 text-md">
				<LoaderCircle size={14} class="animate-spin flex-none" />
				planning the promotion…
			</div>
		{:else if planError}
			<div class="text-status-danger text-md">{planError}</div>
		{:else if planned}
			{#if planned.up_to_date}
				<span class="text-text-muted text-md">
					{target.name} already runs this revision; there is nothing to promote.
				</span>
			{:else}
				{@const changes = planned.plan?.changes ?? []}
				{@const values = planned.plan?.values ?? []}
				<div
					class="border-border-subtle divide-border-subtle max-h-60 divide-y overflow-y-auto rounded-xl border"
				>
					{#each changes as change, i (`c${i}`)}
						<div class="flex items-center gap-2.5 px-3 py-1.75 font-mono text-sm">
							<span class="w-14 flex-none {actionClass(change.action, change.destructive)}">
								{change.action}
							</span>
							<span class="text-text-primary">{change.service}</span>
							{#if change.detail}
								<span class="text-text-faint min-w-0 truncate" title={change.detail}>
									{change.detail}
								</span>
							{/if}
							{#if change.destructive}
								<span class="text-status-danger ml-auto flex-none text-xs">destructive</span>
							{/if}
						</div>
					{/each}
					{#each values as value, i (`v${i}`)}
						<div class="flex items-center gap-2.5 px-3 py-1.75 font-mono text-sm">
							<span class="w-14 flex-none {actionClass(value.action)}">{value.action}</span>
							<span class="text-text-primary">{value.name}</span>
							<span class="text-text-faint">value</span>
						</div>
					{/each}
				</div>
			{/if}
		{/if}
	</div>

	<div
		class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3"
	>
		<Button variant="ghost" disabled={promoting} onclick={back}>Back</Button>
		<Button variant="primary" busy={promoting} disabled={!ready} onclick={promote}>Promote</Button>
	</div>
{/if}
