<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	// Finder anatomy like AddMemberModal: step one searches the project's
	// environments, step two states the promotion and will grow the promote
	// options when they exist.
	export const modalOptions = {
		label: 'Promote',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Promote flow started from the source environment: find the target,
	// review the server's plan, confirm. A promotion reuses artifacts by
	// construction, so the deployment is opened and completed right here and
	// the run continues on the server, followed in the run side panel. The
	// server stays the authority on eligibility; the finder only explains
	// the refusals it can already see.
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import Layers from '@lucide/svelte/icons/layers';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import Lock from '@lucide/svelte/icons/lock';
	import Rocket from '@lucide/svelte/icons/rocket';
	import Search from '@lucide/svelte/icons/search';
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
		/** All environments of the project, for the target finder. */
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

	let query = $state('');
	let active = $state(0);
	let input = $state<HTMLInputElement | null>(null);
	let target = $state<Environment | null>(null);
	let planning = $state(false);
	let planError = $state('');
	let planned = $state<PlanResult | null>(null);
	// undefined while loading, null when the environment runs nothing.
	let sourceRev = $state<string | null | undefined>(undefined);
	let targetRev = $state<string | null | undefined>(undefined);
	let promoting = $state(false);

	// A finder opens ready to type; refocus when Change returns to step one.
	$effect(() => {
		if (!target) input?.focus();
	});

	// Pickable targets first; refused rows sink below them, kept visible
	// with their reasons so the routing policy stays legible.
	const matches = $derived.by(() => {
		const q = query.trim().toLowerCase();
		const named = candidates.filter((e) => !q || e.name.toLowerCase().includes(q));
		return [...named.filter((e) => !refusal(e)), ...named.filter((e) => refusal(e))];
	});
	// Keyboard navigation walks the pickable rows only; they lead the list,
	// so the active index counts from the top.
	const eligible = $derived(matches.filter((e) => !refusal(e)));

	const short = (checksum: string) => checksum.slice(0, 8);

	function onSearchKey(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			active = Math.min(active + 1, eligible.length - 1);
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			active = Math.max(active - 1, 0);
		} else if (e.key === 'Enter' && eligible[active]) {
			e.preventDefault();
			pick(eligible[active]);
		}
	}

	function pick(t: Environment) {
		target = t;
		planning = true;
		planError = '';
		planned = null;
		targetRev = undefined;
		// The revision line is decoration next to the plan; a failed status
		// read just leaves it out.
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
		query = '';
		active = 0;
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

	// One source status read on open: it renders the revision line and,
	// when the source runs nothing, the blocked state that replaces the
	// finder (a promotion moves the running revision, so no target can
	// work). A failed read leaves the state unknown and the flow open; the
	// server still refuses at plan time.
	// svelte-ignore state_referenced_locally
	void api
		.get<EnvironmentStatus>(`/v1/environments/${source.id}/status`)
		.then((s) => (sourceRev = s.active_revision?.checksum ?? null))
		.catch(() => {});

	// The habitual route, recorded by the server on every promotion from
	// this source. Open straight on it; Change returns to the finder,
	// where a badge marks it.
	const lastUsed = candidates.find((e) => e.name === source.last_promotion_target) ?? null;
	if (lastUsed && !refusal(lastUsed)) pick(lastUsed);
</script>

{#if sourceRev === null}
	<ModalHeader title={source.name} mono>
		Promote the revision running here to another environment.
	</ModalHeader>
	<div class="flex flex-col items-center gap-1.5 px-5.5 py-9 text-center">
		<Rocket size={18} class="text-text-ghost" />
		<p class="text-text-primary text-base font-medium">
			Nothing is running in {source.name} yet.
		</p>
		<p class="text-text-muted text-md">
			A promotion moves the running revision; deploy into {source.name} first.
		</p>
	</div>
	<div
		class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3"
	>
		<Button variant="secondary" onclick={() => close(false)}>Close</Button>
	</div>
{:else if !target}
	<div class="border-border-subtle flex items-center gap-2.5 border-b px-4 py-3">
		<Search size={17} class="text-text-ghost flex-none" />
		<input
			bind:this={input}
			bind:value={query}
			type="search"
			name="environment-search"
			autocomplete="off"
			spellcheck="false"
			placeholder="Promote {source.name} to…"
			aria-label="Search environments"
			class="text-text-primary w-full bg-transparent text-lg focus:outline-none"
			oninput={() => (active = 0)}
			onkeydown={onSearchKey}
		/>
		<kbd
			class="font-mono border-border-strong text-text-ghost flex-none rounded-[6px] border px-1.25 py-px text-xs"
		>
			esc
		</kbd>
	</div>

	<div class="max-h-80 min-h-28 flex-1 overflow-y-auto p-2">
		{#if candidates.length === 0}
			<div class="flex flex-col items-center gap-1.5 px-3 py-7 text-center">
				<Layers size={18} class="text-text-ghost" />
				<p class="text-text-muted text-base">No other environments in this project yet.</p>
			</div>
		{:else if matches.length === 0}
			<div class="flex flex-col items-center gap-1.5 px-3 py-7 text-center">
				<Layers size={18} class="text-text-ghost" />
				<p class="text-text-muted text-base">No environment matches this search.</p>
			</div>
		{:else}
			{#each matches as t (t.id)}
				{@const reason = refusal(t)}
				{#if reason}
					<div class="flex w-full items-center gap-2.5 rounded-[11px] px-3 py-2.25" title={reason}>
						{#if t.access === 'none'}
							<Lock size={12} class="text-text-ghost flex-none" />
						{/if}
						<span class="font-mono text-text-ghost text-md">{t.name}</span>
						<span class="text-text-faint ml-auto min-w-0 truncate text-sm">{reason}</span>
					</div>
				{:else}
					{@const idx = eligible.indexOf(t)}
					<button
						type="button"
						class="flex w-full cursor-pointer items-center gap-2.5 rounded-[11px] px-3 py-2.25 text-left transition-colors {idx ===
						active
							? 'bg-white/6'
							: 'hover:bg-white/4'}"
						onclick={() => pick(t)}
						onpointerenter={() => (active = idx)}
					>
						<span class="font-mono text-text-primary text-md">{t.name}</span>
						{#if t.id === lastUsed?.id}
							<Pill text="last used" />
						{/if}
						{#if t.settings?.deploy_policy === 'promote-only'}
							<Pill text="protected" tone="warning" />
						{/if}
						<ArrowRight
							size={13}
							class="ml-auto flex-none transition-opacity {idx === active
								? 'text-text-tertiary opacity-100'
								: 'opacity-0'}"
						/>
					</button>
				{/if}
			{/each}
		{/if}
	</div>

	<div
		class="border-border-subtle bg-surface-raised/50 text-text-ghost flex items-center gap-3 border-t px-4 py-2 text-xs"
	>
		<span class="flex items-center gap-1">
			<kbd class="font-mono border-border-strong rounded-[5px] border px-1 py-px">↑↓</kbd> navigate
		</span>
		<span class="flex items-center gap-1">
			<kbd class="font-mono border-border-strong rounded-[5px] border px-1 py-px">↵</kbd> select
		</span>
	</div>
{:else}
	<ModalHeader
		title="Promote {source.name} to {target.name}"
		description="The revision running in {source.name} moves over; artifacts are reused, nothing rebuilds."
	/>

	<div class="flex flex-col gap-3.5 px-5.5 py-4">
		<div class="border-border-subtle flex items-center gap-3 rounded-xl border px-3 py-2.5">
			<span class="flex min-w-0 items-center gap-2.5">
				<span class="font-mono text-text-secondary text-md">{source.name}</span>
				<ArrowRight size={13} class="text-text-ghost flex-none" />
				<span class="font-mono text-text-primary text-md">{target.name}</span>
				{#if target.settings?.deploy_policy === 'promote-only'}
					<Pill text="protected" tone="warning" />
				{/if}
			</span>
			<Button size="sm" variant="ghost" class="ml-auto flex-none" onclick={back}>
				<ArrowLeft size={13} /> Change
			</Button>
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
			{#each planned.warnings ?? [] as warning (warning.code)}
				<p role="status" class="text-status-warning text-sm">{warning.message}</p>
			{/each}
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
		<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
		<Button variant="primary" busy={promoting} disabled={!ready} onclick={promote}>Promote</Button>
	</div>
{/if}
