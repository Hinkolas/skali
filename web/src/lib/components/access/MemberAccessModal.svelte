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
	import Lock from '@lucide/svelte/icons/lock';
	import { api, ApiError } from '$lib/api/client';
	import { PROJECT_ROLES, ROLES, ROLE_HINT, requiredTitle, roleAtLeast } from '$lib/access';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import RolePicker, { type RoleOption } from './RolePicker.svelte';
	import type { AuthUser } from '$lib/types/auth';
	import type { Environment, Member, Project } from '$lib/types/project';

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

	const projectRoleOptions: RoleOption[] = PROJECT_ROLES.map((r) => ({
		value: r,
		hint: ROLE_HINT[r]
	}));
	const INHERIT = 'inherit';
	const cellOptions: RoleOption[] = [
		{ value: INHERIT, label: 'inherit', hint: 'The project role applies, capped by the ceiling.' },
		...ROLES.map((r) => ({ value: r, hint: ROLE_HINT[r] }))
	];

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
	</ModalHeader>

	<div class="flex flex-col gap-4 overflow-y-auto px-5.5 py-4">
		<div class="flex flex-col gap-1.5">
			<span class="text-text-tertiary text-base font-medium">Project role</span>
			{#if member.instance_admin}
				<div>
					<span class="text-text-secondary font-mono inline-flex items-center text-md">
						admin <span class="text-text-ghost ml-1 text-xs">(instance)</span>
					</span>
				</div>
				<span class="text-text-muted text-base">
					Instance admins are admin everywhere; the membership is informational.
				</span>
			{:else}
				<div>
					<RolePicker
						value={member.role}
						options={projectRoleOptions}
						label="Project role of {member.email}"
						disabled={!canEdit}
						title={canEdit ? undefined : readOnlyTitle}
						busy={!!busy['project']}
						onchange={setProjectRole}
					/>
				</div>
				<span class="text-text-muted text-base">
					Inherited by every environment, capped by each environment's ceiling.
				</span>
			{/if}
		</div>

		<div class="flex flex-col gap-1.5">
			<span class="text-text-tertiary text-base font-medium">Environment roles</span>
			<div class="border-border-subtle divide-border-subtle divide-y rounded-xl border">
				{#each environments as env (env.id)}
					{@const cell = member.environments?.[env.name] ?? null}
					<div class="flex min-h-11.5 items-center gap-2 px-3 py-1.5">
						<span class="flex items-center gap-1.5">
							{#if env.access === 'none'}
								<Lock size={12} class="text-text-ghost flex-none" />
							{/if}
							<span class="font-mono text-text-primary text-md">{env.name}</span>
							{#if env.settings && env.settings.max_role !== 'admin'}
								<span class="font-mono text-text-ghost text-xs" title="ceiling for inherited roles">
									max {env.settings.max_role}
								</span>
							{/if}
						</span>
						<span class="ml-auto flex items-center gap-1.5">
							{#if !cell}
								<span class="text-text-ghost px-2 font-mono text-md">-</span>
							{:else if member.instance_admin}
								<span class="text-text-secondary px-2 font-mono text-md">admin</span>
							{:else}
								{#if cell.cell === null}
									<span
										class="text-text-ghost font-mono text-xs"
										title="inherited from the project role, capped by the ceiling"
									>
										{cell.role}
									</span>
								{:else if cell.cell !== cell.role}
									<span class="text-text-ghost font-mono text-xs">= {cell.role}</span>
								{/if}
								<RolePicker
									value={cell.cell ?? INHERIT}
									options={cellOptions}
									label="Role of {member.email} on {env.name}"
									disabled={!canEdit}
									title={canEdit ? undefined : readOnlyTitle}
									busy={!!busy[env.name]}
									onchange={(value) => setCell(env, value)}
								/>
							{/if}
						</span>
					</div>
				{:else}
					<div class="font-mono text-text-ghost px-3 py-2 text-xs">no environments yet</div>
				{/each}
			</div>
			<span class="text-text-muted text-base">
				An explicit role replaces the inherited one and ignores the ceiling; inherit follows the
				project role.
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
