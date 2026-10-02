<script lang="ts">
	import { relativeTime } from '$lib/format';
	import type { DatabasePoolMember } from '$lib/types/pools';
	import Pill from '$lib/components/ui/Pill.svelte';

	// The instance pods of one pool: their role, node, restarts and age.
	// The primary sorts first; the API already orders that way, this keeps
	// it so whatever the source.
	let { members: input, observed }: { members: DatabasePoolMember[]; observed: boolean } = $props();

	const members = $derived(
		input.toSorted(
			(a, b) =>
				Number(b.role === 'primary') - Number(a.role === 'primary') || a.name.localeCompare(b.name)
		)
	);

	function memberDot(member: DatabasePoolMember): string {
		if (member.ready) return 'bg-status-success';
		if (member.phase === 'Failed') return 'bg-status-danger';
		return 'bg-status-warning';
	}
	function memberState(member: DatabasePoolMember): string {
		return member.ready ? 'ready' : member.phase.toLowerCase();
	}
</script>

{#if members.length > 0}
	<div class="border-border-default overflow-hidden rounded-[11px] border">
		{#each members as member (member.name)}
			<div
				class="border-border-subtle grid grid-cols-[minmax(0,1.8fr)_minmax(0,1fr)_auto_auto_auto] items-center gap-3 border-b px-3 py-2.25 last:border-0"
			>
				<div class="flex min-w-0 items-center gap-2">
					<span
						class="size-[8px] flex-none rounded-full {memberDot(member)}"
						title={memberState(member)}
					></span>
					<span class="text-text-primary truncate font-mono text-sm">{member.name}</span>
				</div>
				<span class="text-text-faint truncate font-mono text-xs" title="node">
					{member.node ?? 'unscheduled'}
				</span>
				<span
					class="font-mono text-xs {member.restarts > 0
						? 'text-status-warning'
						: 'text-text-faint'}"
					title="restarts"
				>
					{member.restarts} restart{member.restarts === 1 ? '' : 's'}
				</span>
				<span class="text-text-faint font-mono text-xs" title="started">
					{relativeTime(member.started_at)}
				</span>
				<span class="flex w-16 justify-end">
					{#if member.role === 'primary'}
						<Pill text="primary" tone="success" />
					{:else if member.role === 'replica'}
						<Pill text="replica" />
					{/if}
				</span>
			</div>
		{/each}
	</div>
{:else}
	<div
		class="border-border-strong grid flex-1 place-items-center rounded-[11px] border border-dashed py-6"
	>
		<div class="flex flex-col gap-1.5 text-center">
			<div class="text-text-muted text-base">
				{observed ? 'No instances running' : 'No instances observed'}
			</div>
			<div class="font-mono text-text-faint text-md">
				{observed
					? 'the pool has no pods right now'
					: 'the daemon has no cluster to read pods from'}
			</div>
		</div>
	</div>
{/if}
