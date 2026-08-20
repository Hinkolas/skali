<script lang="ts">
	// The project's members, one calm row each: identity, override summary,
	// project role, and a Manage button. Per-environment detail lives in
	// MemberAccessModal, so the list stays the same width whether the project
	// has one environment or twelve. Project admins edit; everyone else reads.
	import { invalidateAll } from '$app/navigation';
	import Plus from '@lucide/svelte/icons/plus';
	import { api, ApiError } from '$lib/api/client';
	import { PROJECT_ROLES, ROLE_HINT, requiredTitle } from '$lib/access';
	import { modal } from '$lib/stores/modal.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import AddMemberModal, { modalOptions as addMemberModalOptions } from './AddMemberModal.svelte';
	import MemberAccessModal, {
		modalOptions as memberAccessModalOptions
	} from './MemberAccessModal.svelte';
	import RolePicker, { type RoleOption } from './RolePicker.svelte';
	import type { AuthUser } from '$lib/types/auth';
	import type { Member, Project } from '$lib/types/project';

	let {
		project,
		members,
		canEdit,
		self
	}: {
		project: Project;
		members: Member[];
		canEdit: boolean;
		self: AuthUser | null;
	} = $props();

	const readOnlyTitle = $derived(requiredTitle('admin', 'project', project.name));

	const projectRoleOptions: RoleOption[] = PROJECT_ROLES.map((r) => ({
		value: r,
		hint: ROLE_HINT[r]
	}));

	// Per-row busy flags keep the rest of the list editable while one call
	// is in flight.
	let busy = $state<Record<string, boolean>>({});

	function setProjectRole(member: Member, role: string) {
		void (async () => {
			busy[member.user_id] = true;
			try {
				await api.put(`/v1/projects/${project.id}/members/${member.user_id}`, { role });
				await invalidateAll();
			} catch (err) {
				toast.error(err instanceof ApiError ? err.message : 'Could not change the role');
			} finally {
				delete busy[member.user_id];
			}
		})();
	}

	function initials(member: Member) {
		const source = member.name || member.email;
		const words = source.trim().split(/\s+/);
		return words.length > 1
			? (words[0][0] + words[words.length - 1][0]).toUpperCase()
			: source.slice(0, 2).toUpperCase();
	}

	// The explicit per-environment roles, for the row summary; the modal has
	// the full picture.
	function overrides(member: Member): [string, string][] {
		return Object.entries(member.environments ?? {})
			.filter(([, access]) => access.cell !== null)
			.map(([env, access]) => [env, access.cell as string]);
	}

	function openMember(member: Member) {
		modal.open(MemberAccessModal, { userId: member.user_id }, memberAccessModalOptions);
	}
</script>

<Card class="p-5">
	<div class="mb-3.5 flex items-center gap-2.5">
		<h3 class="text-text-primary text-xl font-semibold">Members</h3>
		<span class="text-text-ghost text-md">who may do what on {project.name}</span>
		{#if canEdit}
			<div class="ml-auto">
				<Button
					size="sm"
					onclick={() => modal.open(AddMemberModal, { project }, addMemberModalOptions)}
				>
					<Plus size={14} /> Add member
				</Button>
			</div>
		{/if}
	</div>

	{#if members.length === 0}
		<div class="font-mono text-text-ghost py-2 text-xs">
			no members yet · instance admins see every project without membership
		</div>
	{:else}
		<div class="grid grid-cols-[auto_minmax(0,1fr)_auto_auto_auto] items-center gap-x-4">
			{#each members as member (member.user_id)}
				{@const isSelf = member.user_id === self?.id}
				{@const explicit = overrides(member)}
				<div
					class="border-border-subtle col-span-full grid grid-cols-subgrid items-center border-b py-2.5 last:border-0"
				>
					<span
						class="text-accent-nav grid size-7 flex-none place-items-center rounded-full bg-linear-135 from-[#37324e] to-[#232030] text-sm font-semibold"
					>
						{initials(member)}
					</span>
					<span class="flex min-w-0 flex-col">
						<span class="text-text-primary truncate text-md">
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
							<span class="text-text-faint font-mono truncate text-xs">{member.email}</span>
						{/if}
					</span>
					<span class="justify-self-end">
						{#if explicit.length > 0}
							<span
								class="font-mono text-text-ghost text-xs"
								title={explicit.map(([env, role]) => `${env}: ${role}`).join(', ')}
							>
								{explicit.length}
								{explicit.length === 1 ? 'override' : 'overrides'}
							</span>
						{/if}
					</span>
					<span>
						{#if member.instance_admin}
							<span
								class="text-text-secondary font-mono inline-flex items-center px-2 py-1 text-md"
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
								busy={!!busy[member.user_id]}
								onchange={(role) => setProjectRole(member, role)}
							/>
						{/if}
					</span>
					<span class="justify-self-end">
						<Button size="sm" variant="ghost" onclick={() => openMember(member)}>
							{canEdit ? 'Manage' : 'View'}
						</Button>
					</span>
				</div>
			{/each}
		</div>
	{/if}

	{#if !canEdit}
		<p class="text-text-ghost mt-3.5 text-xs">{readOnlyTitle} to change members.</p>
	{/if}
</Card>
