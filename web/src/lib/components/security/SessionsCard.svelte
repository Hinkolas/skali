<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import X from '@lucide/svelte/icons/x';
	import { api, ApiError } from '$lib/api/client';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import type { SessionInfo } from '$lib/types/auth';
	import Table from '$lib/components/ui/Table.svelte';

	let { sessions }: { sessions: SessionInfo[] } = $props();

	const grid = 'grid-cols-[2.2fr_1fr_1fr_1fr_44px]';

	function formatDate(iso: string): string {
		return new Date(iso).toLocaleDateString(undefined, {
			year: 'numeric',
			month: 'short',
			day: 'numeric'
		});
	}

	function revoke(session: SessionInfo) {
		dialog.confirm({
			title: 'Revoke this session?',
			description: 'The device is signed out immediately. Revoking is never sudo-gated.',
			confirmLabel: 'Revoke session',
			variant: 'danger',
			onConfirm: async () => {
				try {
					await api.del(`/v1/auth/sessions/${session.id}`);
					toast.success('Session revoked');
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not revoke the session');
					throw err; // keep the dialog open
				}
			}
		});
	}
</script>

<div>
	<h2 class="text-text-primary mb-2.5 text-[14px] font-semibold tracking-tight">Active sessions</h2>
	<Table columns={['Device', 'IP address', 'Signed in', 'Expires', '']} {grid}>
		{#each sessions as session (session.id)}
			<div
				class="border-border-subtle grid items-center border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 {grid}"
			>
				<div class="flex min-w-0 items-center gap-2">
					<span class="text-text-secondary truncate text-[12.5px]" title={session.user_agent}>
						{session.user_agent || 'Unknown device'}
					</span>
					{#if session.current}
						<span
							class="font-mono bg-accent/15 text-accent-light flex-none rounded-full px-2 py-0.5 text-[9.5px]"
						>
							current
						</span>
					{/if}
				</div>
				<div class="font-mono text-text-muted truncate text-[11px]">
					{session.ip_address || '—'}
				</div>
				<div class="font-mono text-text-muted text-[11px]">{formatDate(session.created_at)}</div>
				<div class="font-mono text-text-muted text-[11px]">{formatDate(session.expires_at)}</div>
				<div class="flex justify-end">
					{#if !session.current}
						<button
							type="button"
							onclick={() => revoke(session)}
							class="text-text-ghost hover:text-status-danger cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5"
							aria-label="Revoke session"
							title="Revoke"
						>
							<X size={14} />
						</button>
					{/if}
				</div>
			</div>
		{/each}
	</Table>
</div>
