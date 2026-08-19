<script lang="ts">
	// Members x environments: the project role per member and the effective
	// role on every environment the viewer may read, as the API renders it
	// (the same rule that gates every request). Project admins edit inline;
	// everyone else reads. Sudo-gated writes run on the page, never inside a
	// modal (the reauth prompt needs the single modal slot), so removal
	// confirms first and calls after.
	import { invalidateAll } from '$app/navigation';
	import Lock from '@lucide/svelte/icons/lock';
	import Plus from '@lucide/svelte/icons/plus';
	import { api, ApiError } from '$lib/api/client';
	import { PROJECT_ROLES, ROLES, ROLE_HINT, requiredTitle } from '$lib/access';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';
	import RolePicker, { type RoleOption } from './RolePicker.svelte';
	import type { AuthUser } from '$lib/types/auth';
	import type { AccessRole, Environment, Member, Project } from '$lib/types/project';

	let {
		project,
		environments,
		members,
		canEdit,
		self
	}: {
		project: Project;
		environments: Environment[];
		members: Member[];
		canEdit: boolean;
		self: AuthUser | null;
	} = $props();

	const readOnlyTitle = $derived(requiredTitle('admin', 'project', project.name));

	const projectRoleOptions: RoleOption[] = PROJECT_ROLES.map((r) => ({
		value: r,
		hint: ROLE_HINT[r]
	}));
	const INHERIT = 'inherit';
	const cellOptions: RoleOption[] = [
		{ value: INHERIT, label: 'inherit', hint: 'The project role applies, capped by the ceiling.' },
		...ROLES.map((r) => ({ value: r, hint: ROLE_HINT[r] }))
	];

	// Per-row busy flags keep the rest of the grid editable while one call
	// is in flight.
	let busy = $state<Record<string, boolean>>({});
	let newEmail = $state('');
	let newRole = $state<AccessRole>('read');
	let adding = $state(false);

	function key(userID: string, env?: string) {
		return env ? `${userID}:${env}` : userID;
	}

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

	function setProjectRole(member: Member, role: string) {
		void run(
			key(member.user_id),
			() => api.put(`/v1/projects/${project.id}/members/${member.user_id}`, { role }),
			'Could not change the role'
		);
	}

	function setCell(member: Member, env: Environment, value: string) {
		const id = key(member.user_id, env.name);
		if (value === INHERIT) {
			void run(
				id,
				() => api.del(`/v1/environments/${env.id}/access/${member.user_id}`),
				'Could not drop the role'
			);
			return;
		}
		void run(
			id,
			() => api.put(`/v1/environments/${env.id}/access/${member.user_id}`, { role: value }),
			'Could not set the role'
		);
	}

	async function remove(member: Member) {
		const ok = await dialog.confirm({
			title: `Remove ${member.email}?`,
			description:
				'They lose every role on this project, including explicit per-environment roles, and the project disappears for them.',
			confirmLabel: 'Remove member',
			variant: 'danger'
		});
		if (!ok) return;
		await run(
			key(member.user_id),
			() => api.del(`/v1/projects/${project.id}/members/${member.user_id}`),
			'Could not remove the member'
		);
	}

	async function add() {
		const email = newEmail.trim();
		if (!email) return;
		adding = true;
		try {
			await api.put(`/v1/projects/${project.id}/members/${encodeURIComponent(email)}`, {
				role: newRole
			});
			toast.success(`Added ${email} as ${newRole}`);
			newEmail = '';
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not add the member');
		} finally {
			adding = false;
		}
	}

	// The viewer cannot read a locked environment: its column stays, the
	// cells show nothing.
	function cellOf(member: Member, env: Environment) {
		return member.environments?.[env.name] ?? null;
	}
</script>

<Card class="overflow-x-auto p-5">
	<div class="mb-3.5 flex items-baseline gap-2.5">
		<h3 class="text-text-primary text-xl font-semibold">Members</h3>
		<span class="text-text-ghost text-md">
			project role per member, effective role per environment
		</span>
	</div>

	{#if members.length === 0}
		<div class="font-mono text-text-ghost py-2 text-xs">
			no members yet · instance admins see every project without membership
		</div>
	{:else}
		<table class="w-full border-separate border-spacing-0 text-left">
			<thead>
				<tr class="text-text-ghost text-2xs tracking-wider uppercase">
					<th class="pr-4 pb-2 font-medium">Member</th>
					<th class="pr-4 pb-2 font-medium">Project</th>
					{#each environments as env (env.id)}
						<th class="pr-4 pb-2 font-medium">
							<span class="inline-flex items-center gap-1">
								<span class="font-mono normal-case">{env.name}</span>
								{#if env.access === 'none'}
									<Lock size={11} class="text-text-ghost" />
								{/if}
								{#if env.settings && env.settings.max_role !== 'admin'}
									<span
										class="font-mono text-text-ghost normal-case"
										title="ceiling for inherited roles"
									>
										max {env.settings.max_role}
									</span>
								{/if}
							</span>
						</th>
					{/each}
					{#if canEdit}
						<th class="pb-2"></th>
					{/if}
				</tr>
			</thead>
			<tbody>
				{#each members as member (member.user_id)}
					{@const isSelf = member.user_id === self?.id}
					<tr class="border-border-subtle border-t">
						<td class="border-border-subtle border-t py-2 pr-4 align-middle">
							<span class="flex flex-col">
								<span class="text-text-primary text-md">
									{member.name || member.email}
									{#if isSelf}
										<span
											class="font-mono bg-white/6 text-text-muted ml-1 rounded-full px-1.5 text-2xs"
										>
											you
										</span>
									{/if}
								</span>
								{#if member.name}
									<span class="text-text-faint font-mono text-xs">{member.email}</span>
								{/if}
							</span>
						</td>
						<td class="border-border-subtle border-t py-2 pr-4 align-middle">
							{#if member.instance_admin}
								<span
									class="text-text-secondary inline-flex items-center px-2 py-1 font-mono text-md"
									title="Instance admins are admin everywhere; the membership is informational."
								>
									admin <span class="text-text-ghost ml-1 text-xs">(instance)</span>
								</span>
							{:else}
								<RolePicker
									value={member.role}
									options={projectRoleOptions}
									label="Project role of {member.email}"
									disabled={!canEdit}
									title={canEdit ? undefined : readOnlyTitle}
									busy={!!busy[key(member.user_id)]}
									onchange={(role) => setProjectRole(member, role)}
								/>
							{/if}
						</td>
						{#each environments as env (env.id)}
							{@const cell = cellOf(member, env)}
							<td class="border-border-subtle border-t py-2 pr-4 align-middle">
								{#if !cell}
									<span class="text-text-ghost px-2 font-mono text-md">-</span>
								{:else if member.instance_admin}
									<span class="text-text-secondary px-2 font-mono text-md">admin</span>
								{:else}
									<span class="inline-flex items-center gap-1.5">
										<RolePicker
											value={cell.cell ?? INHERIT}
											options={cellOptions}
											label="Role of {member.email} on {env.name}"
											disabled={!canEdit}
											title={canEdit ? undefined : readOnlyTitle}
											busy={!!busy[key(member.user_id, env.name)]}
											onchange={(value) => setCell(member, env, value)}
										/>
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
									</span>
								{/if}
							</td>
						{/each}
						{#if canEdit}
							<td class="border-border-subtle border-t py-2 text-right align-middle">
								<Button
									size="sm"
									variant="ghost"
									busy={!!busy[key(member.user_id)]}
									onclick={() => remove(member)}
								>
									Remove
								</Button>
							</td>
						{/if}
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}

	{#if canEdit}
		<form
			class="border-border-subtle mt-3.5 flex items-center gap-2 border-t pt-3.5"
			onsubmit={(e) => {
				e.preventDefault();
				void add();
			}}
		>
			<div class="max-w-xs flex-1">
				<TextInput bind:value={newEmail} type="email" placeholder="colleague@example.com" />
			</div>
			<RolePicker
				value={newRole}
				options={projectRoleOptions}
				label="Role of the new member"
				onchange={(role) => (newRole = role as AccessRole)}
			/>
			<Button type="submit" size="sm" busy={adding} disabled={newEmail.trim() === ''}>
				<Plus size={14} /> Add member
			</Button>
			<span class="text-text-ghost ml-auto text-xs">
				Users are created by an instance admin on the Users page.
			</span>
		</form>
	{:else}
		<p class="text-text-ghost mt-3.5 text-xs">{readOnlyTitle} to change members.</p>
	{/if}
</Card>
