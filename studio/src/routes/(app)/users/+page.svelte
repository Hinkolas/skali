<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Search from '@lucide/svelte/icons/search';
	import Pencil from '@lucide/svelte/icons/pencil';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import ShieldCheck from '@lucide/svelte/icons/shield-check';
	import { invalidateAll } from '$app/navigation';
	import { api, ApiError } from '$lib/api/client';
	import { modal } from '$lib/stores/modal.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import type { AuthUser } from '$lib/types/auth';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import NewUserModal, {
		modalOptions as newUserOptions
	} from '$lib/components/users/NewUserModal.svelte';
	import EditUserModal, {
		modalOptions as editUserOptions
	} from '$lib/components/users/EditUserModal.svelte';
	import ResetPasswordModal, {
		modalOptions as resetPasswordOptions
	} from '$lib/components/users/ResetPasswordModal.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const adminCount = $derived(data.users.filter((u) => u.role === 'admin').length);

	// Directory search: the load already holds every user, so filtering is
	// local and instant (the API's ?q= stays for CLI use).
	let query = $state('');
	const shown = $derived.by(() => {
		const q = query.trim().toLowerCase();
		if (!q) return data.users;
		return data.users.filter(
			(u) => u.email.toLowerCase().includes(q) || u.name.toLowerCase().includes(q)
		);
	});

	const userGrid = 'grid-cols-[2fr_1.3fr_0.7fr_1fr_121px]';

	function initials(u: AuthUser): string {
		const base = u.name.trim() || u.email;
		const parts = base.split(/\s+/);
		return parts.length > 1
			? (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
			: base.slice(0, 2).toUpperCase();
	}

	function formatDate(iso: string): string {
		return new Date(iso).toLocaleDateString(undefined, {
			year: 'numeric',
			month: 'short',
			day: 'numeric'
		});
	}

	async function newUser() {
		if (await modal.open<boolean>(NewUserModal, {}, newUserOptions).result) {
			await invalidateAll();
		}
	}

	async function editUser(user: AuthUser) {
		const props = { user, self: user.id === data.user.id };
		if (await modal.open<boolean>(EditUserModal, props, editUserOptions).result) {
			await invalidateAll();
		}
	}

	function resetPassword(user: AuthUser) {
		modal.open(ResetPasswordModal, { user }, resetPasswordOptions);
	}

	function deleteUser(user: AuthUser) {
		dialog.confirm({
			title: `Delete ${user.name.trim() || user.email}?`,
			description:
				'Their account and sessions are removed immediately. Deployed projects are not affected.',
			confirmLabel: 'Delete user',
			variant: 'danger',
			onConfirm: async () => {
				try {
					await api.del(`/v1/users/${user.id}`);
					toast.success(`Deleted ${user.email}`);
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not delete the user');
					throw err; // keep the dialog open
				}
			}
		});
	}
</script>

<svelte:head>
	<title>Users — skali</title>
</svelte:head>

<PageHeader title="Users">
	{#snippet subtitle()}
		{data.users.length} user{data.users.length === 1 ? '' : 's'} · {adminCount} admin{adminCount ===
		1
			? ''
			: 's'}
	{/snippet}
	{#snippet actions()}
		<div class="relative hidden sm:block">
			<Search
				size={15}
				class="text-text-ghost pointer-events-none absolute top-1/2 left-3 -translate-y-1/2"
			/>
			<input
				bind:value={query}
				type="search"
				name="user-search"
				autocomplete="off"
				spellcheck="false"
				data-1p-ignore
				data-lpignore="true"
				data-bwignore
				placeholder="Search users…"
				aria-label="Search users by name or email"
				class="bg-surface-input text-text-primary border-border-strong focus:border-accent/50 focus:ring-3 focus:ring-accent/10 w-40 rounded-[11px] border py-2.25 pr-3 pl-8.5 lg:w-56 text-base transition-[border-color,box-shadow] duration-150 focus:outline-none"
			/>
		</div>
		<Button variant="primary" onclick={newUser}>
			<Plus size={17} strokeWidth={2.5} />
			New user
		</Button>
	{/snippet}
</PageHeader>

<div class="pb-6">
	<Table columns={['User', 'Role', '2FA', 'Created', '']} grid={userGrid}>
		{#if shown.length === 0}
			<div class="text-text-ghost px-4.5 py-6 text-center text-base">
				No user matches "{query.trim()}".
			</div>
		{/if}
		{#each shown as user (user.id)}
			{@const self = user.id === data.user.id}
			<div
				class="border-border-subtle border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 @max-2xl:flex @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-3 @max-2xl:gap-y-2 @2xl:grid @2xl:items-center {userGrid}"
			>
				<div class="flex min-w-0 items-center gap-3 @max-2xl:basis-full">
					<span
						class="text-accent-nav grid size-8 flex-none place-items-center rounded-full bg-linear-135 from-[#37324e] to-[#232030] text-sm font-semibold"
					>
						{initials(user)}
					</span>
					<span class="flex min-w-0 flex-col gap-px">
						<span class="flex items-center gap-2">
							<span class="text-text-primary truncate text-base font-medium">
								{user.name.trim() || '—'}
							</span>
							{#if self}
								<span
									class="font-mono bg-white/6 text-text-muted rounded-full px-2 py-0.5 text-2xs"
								>
									you
								</span>
							{/if}
						</span>
						<span class="text-text-faint truncate text-md">{user.email}</span>
					</span>
				</div>
				<!-- Role, 2FA and created: grid cells on a wide pane, one meta line
				     under the identity on a narrow one (pl-11 = avatar + gap), with
				     the actions at its end so a long name never has to truncate. -->
				<div
					class="@2xl:contents @max-2xl:order-1 @max-2xl:flex @max-2xl:min-w-0 @max-2xl:flex-1 @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-4 @max-2xl:gap-y-1 @max-2xl:pl-11"
				>
					<div class="flex flex-wrap items-center gap-1.5">
						<span
							class="font-mono rounded-full px-2 py-0.5 text-2xs whitespace-nowrap {user.role ===
							'admin'
								? 'text-accent-light bg-accent/15'
								: 'text-text-muted bg-white/6'}"
						>
							{user.role}
						</span>
						{#if user.role === 'member' && user.create_projects}
							<span
								class="font-mono bg-white/6 text-text-muted rounded-full px-2 py-0.5 text-2xs whitespace-nowrap"
								title="May create projects and becomes admin of them"
							>
								creates projects
							</span>
						{/if}
					</div>
					<div>
						{#if user.two_factor_enabled}
							<span class="text-status-success flex items-center gap-1.5 text-md">
								<ShieldCheck size={14} />
								enabled
							</span>
						{:else}
							<span class="text-text-ghost text-md @max-2xl:hidden">—</span>
						{/if}
					</div>
					<div class="font-mono text-text-muted text-sm">{formatDate(user.created_at)}</div>
				</div>
				<div class="flex items-center justify-end gap-1 @max-2xl:order-1 @max-2xl:ml-auto">
					<button
						type="button"
						onclick={() => editUser(user)}
						class="text-text-ghost hover:text-text-secondary cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5"
						aria-label="Edit {user.email}"
						title="Edit"
					>
						<Pencil size={15} />
					</button>
					<button
						type="button"
						onclick={() => resetPassword(user)}
						class="text-text-ghost hover:text-text-secondary cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5"
						aria-label="Reset password for {user.email}"
						title="Reset password"
					>
						<KeyRound size={15} />
					</button>
					<button
						type="button"
						disabled={self}
						onclick={() => deleteUser(user)}
						class="rounded-lg p-1.5 transition-colors {self
							? 'text-text-ghost/40 cursor-default'
							: 'text-text-ghost hover:text-status-danger cursor-pointer hover:bg-white/5'}"
						aria-label="Delete {user.email}"
						title={self ? 'You cannot delete your own account' : 'Delete'}
					>
						<Trash2 size={15} />
					</button>
				</div>
			</div>
		{/each}
	</Table>
</div>
