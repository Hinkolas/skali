<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Environment settings',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Modal editor for one environment's access ceiling, deploy policy,
	// promotion sources, and priority. Each control is shaped like the setting
	// it edits: the ceiling is a rung on the role ladder, policy and priority
	// are switches with contextual copy, sources are toggle chips. Saves a
	// diff with PATCH and closes; the sudo reauth prompt layers above this
	// modal on the stack.
	import { slide } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { invalidateAll } from '$app/navigation';
	import Plus from '@lucide/svelte/icons/plus';
	import X from '@lucide/svelte/icons/x';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, ApiError } from '$lib/api/client';
	import { ROLES, ROLE_RANK, ROLE_HINT, requiredTitle } from '$lib/access';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Menu from '$lib/components/ui/Menu.svelte';
	import MenuItem from '$lib/components/ui/MenuItem.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import type { AccessRole, Environment, EnvironmentSettings } from '$lib/types/project';

	let {
		environment,
		environments,
		canEdit,
		instanceAdmin,
		close
	}: {
		environment: Environment;
		/** All environments of the project, for the promotion sources. */
		environments: Environment[];
		canEdit: boolean;
		instanceAdmin: boolean;
		close: (saved?: boolean) => void;
	} = $props();

	// svelte-ignore state_referenced_locally
	const settings = environment.settings as EnvironmentSettings;
	// svelte-ignore state_referenced_locally
	const others = environments.filter((e) => e.id !== environment.id);
	// svelte-ignore state_referenced_locally
	const readOnlyTitle = requiredTitle('admin', 'environment', environment.name);

	// Seeded once from the open-time snapshot: the modal closes on save, so it
	// never has to track a reload underneath it.
	let maxRole = $state(settings.max_role);
	let deployPolicy = $state(settings.deploy_policy);
	let promoteFrom = $state<string[]>([...settings.promote_from]);
	let priority = $state(settings.priority);
	let saving = $state(false);

	const isProtected = $derived(deployPolicy === 'promote-only');
	// Tokens render straight from the value, so a stale name (a deleted
	// environment) stays visible and removable; the picker offers the rest.
	const remaining = $derived(others.filter((o) => !promoteFrom.includes(o.name)));

	// One contextual sentence per control instead of a static paragraph each.
	const ceilingHint = $derived(
		maxRole === 'admin'
			? 'No ceiling: project roles apply here in full.'
			: maxRole === 'none'
				? 'Locked for inherited roles: without an explicit grant, members see this environment by name only.'
				: `Project roles are capped at ${maxRole} here; explicit grants and project admins are never capped.`
	);
	const policyHint = $derived(
		isProtected
			? 'Direct deploys are refused: revisions arrive by promotion, rollback, or a recorded admin bypass.'
			: 'Deploys land here directly; promotions and rollbacks work too.'
	);
	const priorityHint = $derived(
		priority === 'high'
			? 'Keeps running when resources are tight; inheriting members start read-only.'
			: 'Yields to high priority environments when resources are tight.'
	);

	// Gate on the saved value, not the live one, so lowering high to normal
	// stays reversible until the save actually happens.
	const priorityEditable = $derived(canEdit && (instanceAdmin || settings.priority === 'high'));
	const priorityTitle = $derived(
		!canEdit
			? readOnlyTitle
			: priorityEditable
				? undefined
				: 'instance admin required to raise an environment to high priority'
	);

	const dirty = $derived(
		maxRole !== settings.max_role ||
			deployPolicy !== settings.deploy_policy ||
			priority !== settings.priority ||
			promoteFrom.join(',') !== settings.promote_from.join(',')
	);

	function rungClass(role: AccessRole): string {
		const selected = role === maxRole;
		const within = ROLE_RANK[role] <= ROLE_RANK[maxRole];
		if (selected) return 'inset-ring inset-ring-accent/25 bg-accent/15 text-accent-nav';
		if (within) return `bg-accent/6 text-text-secondary ${canEdit ? 'hover:bg-accent/10' : ''}`;
		return `text-text-ghost ${canEdit ? 'hover:bg-white/4 hover:text-text-tertiary' : ''}`;
	}

	function togglePromoteFrom(name: string) {
		promoteFrom = promoteFrom.includes(name)
			? promoteFrom.filter((n) => n !== name)
			: [...promoteFrom, name];
	}

	async function save() {
		if (!dirty || saving) return;
		const patch: Partial<{
			max_role: AccessRole;
			deploy_policy: string;
			promote_from: string[];
			priority: string;
		}> = {};
		if (maxRole !== settings.max_role) patch.max_role = maxRole;
		if (deployPolicy !== settings.deploy_policy) patch.deploy_policy = deployPolicy;
		if (promoteFrom.join(',') !== settings.promote_from.join(',')) patch.promote_from = promoteFrom;
		if (priority !== settings.priority) patch.priority = priority;
		saving = true;
		try {
			await api.patch(`/v1/environments/${environment.id}`, patch);
			toast.success(
				`Updated ${environment.name}`,
				patch.priority
					? { description: 'Application pods roll onto the new priority class.' }
					: undefined
			);
			await invalidateAll();
			close(true);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not update the environment');
		} finally {
			saving = false;
		}
	}
</script>

{#snippet switchPill(on: boolean)}
	<span
		class="relative inline-flex h-5 w-9 flex-none items-center rounded-full transition-colors {on
			? 'bg-accent'
			: 'bg-white/12'}"
	>
		<span
			class="bg-surface-base inline-block size-4 rounded-full shadow transition-transform {on
				? 'translate-x-4.5'
				: 'translate-x-0.5'}"
		></span>
	</span>
{/snippet}

<ModalHeader title={environment.name} mono>
	Environment settings{#if !canEdit}
		· {readOnlyTitle} to change these.{/if}
</ModalHeader>

<div class="divide-border-subtle flex flex-col divide-y overflow-y-auto">
	<!-- Ceiling: the ladder itself is the control; rungs at or below the
	     ceiling read as reachable, rungs above sit outside it. -->
	<div class="flex flex-col gap-2.5 px-5.5 py-4">
		<span class="text-text-primary text-base font-medium">Ceiling for inherited roles</span>
		<div
			role="radiogroup"
			aria-label="Ceiling of {environment.name}"
			class="border-border-strong grid grid-cols-5 overflow-hidden rounded-lg border"
		>
			{#each ROLES as role, i (role)}
				<button
					type="button"
					role="radio"
					aria-checked={role === maxRole}
					disabled={!canEdit}
					title={canEdit ? ROLE_HINT[role] : readOnlyTitle}
					onclick={() => (maxRole = role)}
					class="py-1.75 font-mono text-sm transition-colors focus-visible:z-10 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/70 {i >
					0
						? 'border-border-subtle border-l'
						: ''} {rungClass(role)} {canEdit ? 'cursor-pointer' : 'cursor-default'}"
				>
					{role}
				</button>
			{/each}
		</div>
		<span class="text-text-muted text-md leading-relaxed">{ceilingHint}</span>
	</div>

	<!-- Deploy policy: protection is on or off; the sources only exist while
	     it is on, so they live inside the switched-on state. -->
	<div class="flex flex-col px-5.5 py-4">
		<button
			type="button"
			role="switch"
			aria-checked={isProtected}
			disabled={!canEdit}
			title={canEdit ? undefined : readOnlyTitle}
			onclick={() => (deployPolicy = isProtected ? 'direct' : 'promote-only')}
			class="flex w-full items-center justify-between gap-4 rounded-lg text-left focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-accent/70 {canEdit
				? 'cursor-pointer'
				: 'cursor-default'}"
		>
			<span class="flex flex-col gap-0.5">
				<span class="text-text-primary text-base font-medium">Deploy policy</span>
				<span class="text-text-muted text-md leading-relaxed">{policyHint}</span>
			</span>
			<span class="flex flex-none items-center gap-2.5">
				<span class="font-mono text-md {isProtected ? 'text-accent-light' : 'text-text-faint'}">
					{deployPolicy}
				</span>
				{@render switchPill(isProtected)}
			</span>
		</button>
		{#if isProtected}
			<div transition:slide={{ duration: 180, easing: cubicOut }}>
				<div class="border-border-subtle mt-3.5 ml-1 flex flex-col gap-2 border-l pl-4">
					<span class="text-text-tertiary text-md font-medium">Accepts promotions from</span>
					<div class="flex flex-wrap items-center gap-1.5">
						{#each promoteFrom as name (name)}
							<span
								class="border-accent/50 bg-accent/10 text-accent-nav font-mono inline-flex items-center gap-1 rounded-full border py-1 pr-1.5 pl-2.5 text-md"
							>
								{name}
								{#if canEdit}
									<button
										type="button"
										aria-label="Remove {name} from the promotion sources"
										onclick={() => togglePromoteFrom(name)}
										class="text-accent-nav/60 hover:text-accent-nav cursor-pointer rounded-full p-0.5 transition-colors focus-visible:outline-2 focus-visible:outline-accent/70"
									>
										<X size={12} strokeWidth={2.5} />
									</button>
								{/if}
							</span>
						{:else}
							<span
								title="Until a source is added, any environment of the project may promote here."
								class="border-border-strong text-text-faint font-mono inline-flex items-center rounded-full border border-dashed px-2.5 py-1 text-md"
							>
								any environment
							</span>
						{/each}
						{#if canEdit && remaining.length}
							<Menu
								label="Add a promotion source"
								triggerClass="text-text-tertiary hover:bg-white/4 hover:text-text-primary inline-flex cursor-pointer items-center gap-1 rounded-full border border-transparent px-2 py-1 text-md transition-colors focus-visible:outline-2 focus-visible:outline-accent/70"
							>
								{#snippet trigger()}
									<Plus size={13} strokeWidth={2} class="flex-none" />
									{promoteFrom.length ? 'Add' : 'Restrict to'}
								{/snippet}
								{#each remaining as other (other.id)}
									<MenuItem onselect={() => togglePromoteFrom(other.name)}>
										<span class="font-mono text-md">{other.name}</span>
									</MenuItem>
								{/each}
							</Menu>
						{:else if !others.length}
							<span class="text-text-muted text-md">No other environments yet.</span>
						{/if}
					</div>
				</div>
			</div>
		{/if}
	</div>

	<!-- Priority: high or normal, with the pod-roll consequence surfaced only
	     once the value actually changed. -->
	<div class="flex flex-col px-5.5 py-4">
		<button
			type="button"
			role="switch"
			aria-checked={priority === 'high'}
			disabled={!priorityEditable}
			title={priorityTitle}
			onclick={() => (priority = priority === 'high' ? 'normal' : 'high')}
			class="flex w-full items-center justify-between gap-4 rounded-lg text-left focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-accent/70 {priorityEditable
				? 'cursor-pointer'
				: 'cursor-default'}"
		>
			<span class="flex flex-col gap-0.5">
				<span class="text-text-primary text-base font-medium">Priority</span>
				<span class="text-text-muted text-md leading-relaxed">{priorityHint}</span>
			</span>
			<span class="flex flex-none items-center gap-2.5">
				<span
					class="font-mono text-md {priority === 'high' ? 'text-accent-light' : 'text-text-faint'}"
				>
					{priority}
				</span>
				{@render switchPill(priority === 'high')}
			</span>
		</button>
		{#if priority !== settings.priority}
			<div transition:slide={{ duration: 180, easing: cubicOut }}>
				<div class="text-status-warning mt-2.5 flex items-center gap-1.5">
					<TriangleAlert size={13} class="flex-none" />
					<span class="text-md">
						Saving rolls the environment's application pods onto the new priority class.
					</span>
				</div>
			</div>
		{/if}
	</div>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	{#if canEdit}
		<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
		<Button variant="primary" busy={saving} disabled={!dirty} onclick={save}>Save</Button>
	{:else}
		<Button variant="secondary" onclick={() => close(false)}>Close</Button>
	{/if}
</div>
