<script lang="ts">
	// The project's members, one calm row each: identity, explicit
	// per-environment roles, project role, and a Manage button. The row only
	// states; every edit happens in MemberAccessModal, so the list keeps one
	// column width whether the project has one environment or twelve.
	import Plus from '@lucide/svelte/icons/plus';
	import Settings2 from '@lucide/svelte/icons/settings-2';
	import { ROLE_HINT, requiredTitle } from '$lib/access';
	import { modal } from '$lib/stores/modal.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import AddMemberModal, { modalOptions as addMemberModalOptions } from './AddMemberModal.svelte';
	import MemberAccessModal, {
		modalOptions as memberAccessModalOptions
	} from './MemberAccessModal.svelte';
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

<Card class="p-5 pb-2.5">
	<div class="mb-3 flex items-center gap-2.5">
		<h3 class="text-text-primary text-xl font-semibold">Members</h3>
		<span class="text-text-muted text-md">who may do what on {project.name}</span>
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
		<div class="font-mono text-text-faint py-2 text-md">
			no members yet · instance admins see every project without membership
		</div>
	{:else}
		<!-- Badge scale shared by the role and the explicit per-environment
		     roles, so the row's right side reads as one family of states. -->
		<div class="grid grid-cols-[auto_minmax(0,1fr)_minmax(0,18rem)_auto_auto] items-center gap-x-4">
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
					<span class="flex flex-wrap justify-end gap-1.5">
						{#each explicit as [env, role] (env)}
							<span
								class="font-mono inline-flex h-7 items-center rounded-full px-2.5 text-xs {role ===
								'none'
									? 'bg-status-warning/10 text-status-warning'
									: 'bg-white/6 text-text-muted'}"
								title="explicit role on {env}, ignores the ceiling"
							>
								{env}<span class="opacity-50">:</span>{role}
							</span>
						{/each}
					</span>
					<span class="justify-self-end">
						<span
							class="font-mono inline-flex h-7 items-center gap-1 rounded-full px-2.5 text-md {member.instance_admin
								? 'bg-accent/12 text-accent-light'
								: 'bg-white/6 text-text-secondary'}"
							title={member.instance_admin
								? 'Instance admins are admin everywhere; the membership is informational.'
								: ROLE_HINT[member.role]}
						>
							{member.instance_admin ? 'admin' : member.role}
							{#if member.instance_admin}
								<span class="text-accent-light/60 text-xs">instance</span>
							{/if}
						</span>
					</span>
					<span class="justify-self-end">
						<Button
							size="icon"
							variant="ghost"
							ariaLabel="{canEdit ? 'Manage' : 'View'} {member.name || member.email}"
							title={canEdit ? 'Manage access' : 'View access'}
							onclick={() => openMember(member)}
						>
							<Settings2 size={15} />
						</Button>
					</span>
				</div>
			{/each}
		</div>
	{/if}

	{#if !canEdit}
		<p class="text-text-muted mt-3.5 text-md">{readOnlyTitle} to change members.</p>
	{/if}
</Card>
