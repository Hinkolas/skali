<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	// Finder anatomy inside, but placed and animated like the ordinary
	// centered modals (owner preference over the top-hung palette position).
	export const modalOptions = {
		label: 'Add member'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Two steps, like inviting a collaborator on GitHub. Step one is a
	// finder: the user directory (/v1/users) searched as you type, current
	// members filtered out, arrow keys to pick. Step two states the grant:
	// the chosen person and the role ladder spelled out. Users themselves are
	// created by an instance admin on the Users page; this only attaches one
	// to the project.
	import { invalidateAll } from '$app/navigation';
	import { page } from '$app/state';
	import Search from '@lucide/svelte/icons/search';
	import ArrowLeft from '@lucide/svelte/icons/arrow-left';
	import UserRound from '@lucide/svelte/icons/user-round';
	import { api, ApiError } from '$lib/api/client';
	import { PROJECT_ROLES, ROLE_HINT } from '$lib/access';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import type { AccessRole, Member, Project } from '$lib/types/project';

	type DirectoryUser = {
		id: string;
		email: string;
		name: string;
		role: string;
	};

	let { project, close }: { project: Project; close: (added?: boolean) => void } = $props();

	// The page's member list names who is already in; they never appear as
	// candidates.
	const memberIds = $derived(
		new Set(((page.data.members ?? []) as Member[]).map((m) => m.user_id))
	);

	let query = $state('');
	let users = $state<DirectoryUser[]>([]);
	let loaded = $state(false);
	let active = $state(0);
	let selected = $state<DirectoryUser | null>(null);
	let role = $state<AccessRole>('read');
	let busy = $state(false);
	let errorMessage = $state('');
	let input = $state<HTMLInputElement | null>(null);

	// A finder opens ready to type; refocus when Change returns to step one.
	$effect(() => {
		input?.focus();
	});

	// One directory fetch on open, filtered locally as you type: a
	// self-hosted instance's user list is small, and instant filtering
	// beats a round-trip per keystroke. The API's ?q= stays for CLI use.
	api
		.get<{ users: DirectoryUser[] }>('/v1/users')
		.then((res) => {
			users = res.users;
		})
		.catch((err) => {
			errorMessage = err instanceof ApiError ? err.message : 'Could not load users.';
		})
		.finally(() => {
			loaded = true;
		});

	const candidates = $derived.by(() => {
		const q = query.trim().toLowerCase();
		return users.filter(
			(u) =>
				!memberIds.has(u.id) &&
				(!q || u.email.toLowerCase().includes(q) || u.name.toLowerCase().includes(q))
		);
	});

	function onInput() {
		active = 0;
	}

	function onSearchKey(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			active = Math.min(active + 1, candidates.length - 1);
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			active = Math.max(active - 1, 0);
		} else if (e.key === 'Enter' && candidates[active]) {
			e.preventDefault();
			pick(candidates[active]);
		}
	}

	function pick(candidate: DirectoryUser) {
		selected = candidate;
		errorMessage = '';
	}

	function back() {
		selected = null;
		errorMessage = '';
	}

	async function add() {
		if (!selected) return;
		busy = true;
		errorMessage = '';
		try {
			await api.put(`/v1/projects/${project.id}/members/${selected.id}`, { role });
			toast.success(`Added ${selected.name || selected.email} as ${role}`);
			await invalidateAll();
			close(true);
		} catch (err) {
			errorMessage = err instanceof ApiError ? err.message : 'Could not add the member.';
		} finally {
			busy = false;
		}
	}

	function initials(user: DirectoryUser) {
		const source = user.name || user.email;
		const words = source.trim().split(/\s+/);
		return words.length > 1
			? (words[0][0] + words[words.length - 1][0]).toUpperCase()
			: source.slice(0, 2).toUpperCase();
	}
</script>

{#if !selected}
	<div class="border-border-subtle flex items-center gap-2.5 border-b px-4 py-3">
		<Search size={17} class="text-text-ghost flex-none" />
		<!-- type=search + the vendor ignore flags keep password managers from
		     reading "member search" as a login form and offering email autofill. -->
		<input
			bind:this={input}
			bind:value={query}
			type="search"
			name="member-search"
			autocomplete="off"
			spellcheck="false"
			data-1p-ignore
			data-lpignore="true"
			data-bwignore
			placeholder="Add a member to {project.display_name || project.name}…"
			aria-label="Search users by name or email"
			class="text-text-primary w-full bg-transparent text-lg focus:outline-none"
			oninput={onInput}
			onkeydown={onSearchKey}
		/>
		<kbd
			class="font-mono border-border-strong text-text-ghost flex-none rounded-[6px] border px-1.25 py-px text-xs"
		>
			esc
		</kbd>
	</div>

	<div class="max-h-80 min-h-28 flex-1 overflow-y-auto p-2">
		{#if errorMessage}
			<p class="text-status-danger px-3 py-4 text-base">{errorMessage}</p>
		{:else if candidates.length === 0 && loaded}
			<div class="flex flex-col items-center gap-1.5 px-3 py-7 text-center">
				<UserRound size={18} class="text-text-ghost" />
				<p class="text-text-muted text-base">
					{query.trim() ? 'No user matches this search.' : 'Everyone already has a role here.'}
				</p>
				<p class="text-text-muted text-md">
					Users are created by an instance admin on the Users page.
				</p>
			</div>
		{:else}
			{#each candidates as candidate, i (candidate.id)}
				<button
					type="button"
					class="flex w-full cursor-pointer items-center gap-3 rounded-[11px] px-3 py-2 text-left transition-colors {i ===
					active
						? 'bg-white/6'
						: 'hover:bg-white/4'}"
					onclick={() => pick(candidate)}
					onpointerenter={() => (active = i)}
				>
					<span
						class="text-accent-nav grid size-7 flex-none place-items-center rounded-full bg-linear-135 from-[#37324e] to-[#232030] text-sm font-semibold"
					>
						{initials(candidate)}
					</span>
					<span class="flex min-w-0 flex-col">
						<span class="text-text-primary truncate text-base font-medium">
							{candidate.name || candidate.email}
						</span>
						{#if candidate.name}
							<span class="text-text-faint font-mono truncate text-xs">{candidate.email}</span>
						{/if}
					</span>
					{#if candidate.role === 'admin'}
						<span
							class="font-mono bg-white/6 text-text-muted ml-auto flex-none rounded-full px-1.5 py-0.5 text-2xs"
							title="Instance admins are admin everywhere; a membership is informational."
						>
							instance admin
						</span>
					{/if}
				</button>
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
		title="Add {selected.name || selected.email} to {project.display_name || project.name}"
		description="The project role applies on every environment, capped by each environment's ceiling."
	/>

	<div class="flex flex-col gap-3.5 px-5.5 py-4">
		<div class="border-border-subtle flex items-center gap-3 rounded-xl border px-3 py-2.5">
			<span
				class="text-accent-nav grid size-7 flex-none place-items-center rounded-full bg-linear-135 from-[#37324e] to-[#232030] text-sm font-semibold"
			>
				{initials(selected)}
			</span>
			<span class="flex min-w-0 flex-col">
				<span class="text-text-primary truncate text-md">{selected.name || selected.email}</span>
				{#if selected.name}
					<span class="text-text-faint font-mono truncate text-xs">{selected.email}</span>
				{/if}
			</span>
			<Button size="sm" variant="ghost" class="ml-auto flex-none" onclick={back}>
				<ArrowLeft size={13} /> Change
			</Button>
		</div>

		{#if selected.role === 'admin'}
			<p class="text-text-muted text-md">
				This user is an instance admin and already admin everywhere; the membership is
				informational.
			</p>
		{/if}

		<div class="flex flex-col gap-1" role="radiogroup" aria-label="Role of the new member">
			<span class="text-text-tertiary mb-0.5 text-base font-medium">Choose a role</span>
			{#each PROJECT_ROLES as option (option)}
				{@const activeRole = role === option}
				<!-- Rounded rows like the finder list; the chosen role carries the
				     active-nav tint so the radio dot and the row agree. -->
				<label
					class="flex cursor-pointer items-start gap-3 rounded-[11px] px-3 py-2.5 transition-colors {activeRole
						? 'bg-accent/8 inset-ring inset-ring-accent/20'
						: 'hover:bg-white/4'}"
				>
					<input
						type="radio"
						name="member-role"
						value={option}
						checked={activeRole}
						onchange={() => (role = option)}
						class="sr-only"
					/>
					<span
						class="mt-0.75 grid size-4 flex-none place-items-center rounded-full border transition-colors {activeRole
							? 'border-accent'
							: 'border-border-strong'}"
						aria-hidden="true"
					>
						{#if activeRole}
							<span class="bg-accent size-2 rounded-full"></span>
						{/if}
					</span>
					<span class="flex flex-col">
						<span class="text-text-primary font-mono text-md">
							{option}
							{#if option === 'read'}
								<span
									class="border-border-strong text-text-ghost ml-1 rounded-full border px-1.5 text-2xs font-normal"
								>
									base role
								</span>
							{/if}
						</span>
						<span class="text-text-faint text-sm">{ROLE_HINT[option]}</span>
					</span>
				</label>
			{/each}
		</div>

		{#if errorMessage}
			<p class="text-status-danger text-sm">{errorMessage}</p>
		{/if}
	</div>

	<div
		class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3"
	>
		<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
		<Button variant="primary" {busy} onclick={add}>Add member</Button>
	</div>
{/if}
