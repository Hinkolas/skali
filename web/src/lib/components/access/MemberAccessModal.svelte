<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Member access',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// One member's whole standing on the project: the project role plus the
	// effective role on every environment the viewer may read, with explicit
	// overrides edited in place. Reads live page data by user id, so every
	// mutation's invalidateAll() refreshes the modal too; confirms and the
	// sudo reauth prompt layer above it on the modal stack.
	import { invalidateAll } from '$app/navigation';
	import { page } from '$app/state';
	import { slide } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
	import Lock from '@lucide/svelte/icons/lock';
	import { api, ApiError } from '$lib/api/client';
	import {
		PROJECT_ROLES,
		ROLES,
		ROLE_HINT,
		ROLE_RANK,
		requiredTitle,
		roleAtLeast
	} from '$lib/access';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import type { AuthUser } from '$lib/types/auth';
	import type { AccessRole, Environment, Member, Project } from '$lib/types/project';

	let { userId, close }: { userId: string; close: () => void } = $props();

	const project = $derived(page.data.project as Project);
	const environments = $derived((page.data.environments ?? []) as Environment[]);
	const member = $derived(
		((page.data.members ?? []) as Member[]).find((m) => m.user_id === userId)
	);
	const self = $derived(page.data.user as AuthUser | null);
	const canEdit = $derived(roleAtLeast(project.access.role, 'admin'));
	const readOnlyTitle = $derived(requiredTitle('admin', 'project', project.name));

	// The member can disappear underneath the modal (removed in another tab);
	// closing beats rendering a ghost.
	$effect(() => {
		if (!member) close();
	});

	const INHERIT = 'inherit';
	// The per-environment ladder: inherit first, then every explicit rung.
	const CELL_RUNGS: string[] = [INHERIT, ...ROLES];
	const explicitCount = $derived(
		environments.filter((e) => member?.environments?.[e.name]?.cell != null).length
	);

	// One environment row open at a time: the list stays one line per
	// environment however many there are, and the ladder only appears for
	// the row being edited.
	let openEnv = $state<string | null>(null);

	// Rung styling shared by both ladders: the chosen rung is lit, rungs the
	// member effectively holds (at or below the chosen one, or the rung an
	// inherited role resolves to) read as reachable, the rest sit outside.
	function rungClass(selected: boolean, within: boolean): string {
		if (selected) return 'inset-ring inset-ring-accent/25 bg-accent/15 text-accent-nav';
		if (within) return `bg-accent/6 text-text-secondary ${canEdit ? 'hover:bg-accent/10' : ''}`;
		return `text-text-ghost ${canEdit ? 'hover:bg-white/4 hover:text-text-tertiary' : ''}`;
	}
	function projectRungClass(role: AccessRole, current: AccessRole): string {
		return rungClass(role === current, ROLE_RANK[role] <= ROLE_RANK[current]);
	}
	function cellRungClass(rung: string, cell: AccessRole | null, effective: AccessRole): string {
		const chosen = cell ?? INHERIT;
		if (rung === INHERIT) return rungClass(chosen === INHERIT, false);
		const role = rung as AccessRole;
		// `none` is a lock, not a rung anyone holds on the way up, so it only
		// lights when chosen outright.
		const within =
			chosen === INHERIT
				? role === effective
				: role !== 'none' && ROLE_RANK[role] <= ROLE_RANK[chosen as AccessRole];
		return rungClass(rung === chosen, within);
	}
	function cellTitle(rung: string, env: Environment, effective: AccessRole): string {
		if (!canEdit) return readOnlyTitle;
		if (rung === INHERIT)
			return `Follow the project role, capped by the ceiling: ${effective} here.`;
		return ROLE_HINT[rung as AccessRole];
	}

	// Per-control busy flags keep the rest of the modal editable while one
	// call is in flight.
	let busy = $state<Record<string, boolean>>({});
	let removing = $state(false);

	async function run(id: string, call: () => Promise<unknown>, fallback: string) {
		busy[id] = true;
		try {
			await call();
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : fallback);
		} finally {
			delete busy[id];
		}
	}

	function setProjectRole(role: string) {
		void run(
			'project',
			() => api.put(`/v1/projects/${project.id}/members/${userId}`, { role }),
			'Could not change the role'
		);
	}

	function setCell(env: Environment, value: string) {
		if (value === INHERIT) {
			void run(
				env.name,
				() => api.del(`/v1/environments/${env.id}/access/${userId}`),
				'Could not drop the role'
			);
			return;
		}
		void run(
			env.name,
			() => api.put(`/v1/environments/${env.id}/access/${userId}`, { role: value }),
			'Could not set the role'
		);
	}

	async function remove() {
		if (!member) return;
		const email = member.email;
		const ok = await dialog.confirm({
			title: `Remove ${email}?`,
			description:
				'They lose every role on this project, including explicit per-environment roles, and the project disappears for them.',
			confirmLabel: 'Remove member',
			variant: 'danger'
		});
		if (!ok) return;
		removing = true;
		try {
			await api.del(`/v1/projects/${project.id}/members/${userId}`);
			toast.success(`Removed ${email}`);
			close();
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not remove the member');
			removing = false;
		}
	}
</script>

{#if member}
	<ModalHeader title={member.name || member.email}>
		<span class="font-mono">{member.email}</span>
		{#if member.user_id === self?.id}
			· this is you
		{/if}
		{#if !canEdit}
			· {readOnlyTitle} to change these.
		{/if}
	</ModalHeader>

	<div class="divide-border-subtle flex flex-col divide-y overflow-y-auto">
		<!-- Project role: the ladder is the control, the rung's meaning the
		     caption. Instance admins hold admin everywhere; their ladder is
		     pinned and says so. -->
		<div class="flex flex-col gap-2.5 px-5.5 py-4">
			<span class="text-text-primary text-base font-medium">Project role</span>
			<div
				role="radiogroup"
				aria-label="Project role of {member.email}"
				class="border-border-strong grid grid-cols-4 overflow-hidden rounded-lg border {busy[
					'project'
				]
					? 'opacity-60'
					: ''}"
			>
				{#each PROJECT_ROLES as role, i (role)}
					{@const current = member.instance_admin ? 'admin' : member.role}
					<button
						type="button"
						role="radio"
						aria-checked={role === current}
						disabled={!canEdit || member.instance_admin || !!busy['project']}
						title={canEdit ? ROLE_HINT[role] : readOnlyTitle}
						onclick={() => role !== current && setProjectRole(role)}
						class="py-1.75 font-mono text-sm transition-colors focus-visible:z-10 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/70 {i >
						0
							? 'border-border-subtle border-l'
							: ''} {projectRungClass(role, current)} {canEdit && !member.instance_admin
							? 'cursor-pointer'
							: 'cursor-default'}"
					>
						{role}
					</button>
				{/each}
			</div>
			<span class="text-text-muted text-md leading-relaxed">
				{#if member.instance_admin}
					Instance admins are admin everywhere; the membership is informational.
				{:else}
					{ROLE_HINT[member.role]} Inherited by every environment, capped by each environment's ceiling.
				{/if}
			</span>
		</div>

		<!-- Environment roles: a one-line row per environment stating the
		     effective role and where it comes from; opening a row reveals its
		     ladder. Inherit follows the project role through the ceiling; an
		     explicit rung replaces it and ignores the ceiling. -->
		<div class="flex flex-col gap-2.5 px-5.5 py-4">
			<div class="flex items-baseline gap-2.5">
				<span class="text-text-primary text-base font-medium">Environment roles</span>
				<span class="text-text-faint font-mono text-xs">
					{#if explicitCount === 0}
						all inherit the project role
					{:else}
						{explicitCount} explicit · {environments.length - explicitCount} inherit
					{/if}
				</span>
			</div>
			<div
				class="border-border-subtle divide-border-subtle divide-y overflow-hidden rounded-xl border"
			>
				{#each environments as env (env.id)}
					{@const cell = member.environments?.[env.name] ?? null}
					{@const editable = !!cell && !member.instance_admin}
					{@const open = openEnv === env.name}
					<!-- The hover tint covers the whole item, ladder included, so an
					     open row reads as one thing rather than a header with a box
					     hanging off it. -->
					<div class={editable ? 'transition-colors hover:bg-white/2' : ''}>
						<button
							type="button"
							disabled={!editable}
							aria-expanded={editable ? open : undefined}
							onclick={() => (openEnv = open ? null : env.name)}
							class="flex w-full items-center gap-2 px-3 py-2.25 text-left {editable
								? 'cursor-pointer'
								: 'cursor-default'}"
						>
							{#if env.access === 'none'}
								<Lock size={12} class="text-text-ghost flex-none" />
							{/if}
							<span class="font-mono text-text-primary text-md">{env.name}</span>
							{#if env.settings && env.settings.max_role !== 'admin'}
								<span class="font-mono text-text-faint text-xs" title="ceiling for inherited roles">
									max {env.settings.max_role}
								</span>
							{/if}
							<span class="ml-auto flex items-center gap-2">
								{#if !cell}
									<span class="font-mono text-text-ghost text-xs">not visible to you</span>
								{:else if member.instance_admin}
									<span class="font-mono text-text-secondary text-md">admin</span>
								{:else}
									<span
										class="font-mono text-md {cell.role === 'none'
											? 'text-status-warning'
											: 'text-text-secondary'}"
									>
										{cell.role}
									</span>
									{#if cell.cell !== null}
										<span
											class="bg-accent/12 text-accent-light rounded-full px-2 py-0.5 font-mono text-2xs"
											title="set on this environment, ignores the ceiling"
										>
											explicit
										</span>
									{:else}
										<span
											class="bg-white/6 text-text-faint rounded-full px-2 py-0.5 font-mono text-2xs"
											title="follows the project role, capped by the ceiling"
										>
											inherits
										</span>
									{/if}
								{/if}
								{#if editable}
									<ChevronDown
										size={14}
										class="text-text-faint transition-transform duration-200 {open
											? 'rotate-180'
											: ''}"
									/>
								{/if}
							</span>
						</button>
						{#if editable && open && cell}
							<!-- The spacing is padding on the animated wrapper, not margin on
							     the ladder: slide animates padding, while a child's margin
							     would sit outside the height it animates and pop in last. -->
							<div class="px-3 pb-3" transition:slide={{ duration: 180, easing: cubicOut }}>
								<div
									role="radiogroup"
									aria-label="Role of {member.email} on {env.name}"
									class="border-border-strong grid grid-cols-6 overflow-hidden rounded-lg border {busy[
										env.name
									]
										? 'opacity-60'
										: ''}"
								>
									{#each CELL_RUNGS as rung, i (rung)}
										{@const chosen = cell.cell ?? INHERIT}
										<button
											type="button"
											role="radio"
											aria-checked={rung === chosen}
											disabled={!canEdit || !!busy[env.name]}
											title={cellTitle(rung, env, cell.role)}
											onclick={() => rung !== chosen && setCell(env, rung)}
											class="py-1.5 font-mono text-sm transition-colors focus-visible:z-10 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/70 {i >
											0
												? 'border-border-subtle border-l'
												: ''} {cellRungClass(rung, cell.cell, cell.role)} {canEdit
												? 'cursor-pointer'
												: 'cursor-default'}"
										>
											{rung}
										</button>
									{/each}
								</div>
							</div>
						{/if}
					</div>
				{:else}
					<span class="font-mono text-text-faint block px-3 py-2 text-md">no environments yet</span>
				{/each}
			</div>
			<span class="text-text-muted text-md leading-relaxed">
				Changes apply at once. An explicit rung replaces the inherited role and ignores the ceiling;
				inherit follows the project role.
			</span>
		</div>
	</div>

	<div
		class="border-border-subtle bg-surface-raised/50 flex items-center gap-2 border-t px-5.5 py-3"
	>
		{#if canEdit}
			<Button variant="danger" size="sm" busy={removing} onclick={remove}>Remove member</Button>
		{/if}
		<div class="ml-auto">
			<Button variant="secondary" onclick={() => close()}>Done</Button>
		</div>
	</div>
{/if}
