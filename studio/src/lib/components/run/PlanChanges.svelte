<script lang="ts">
	import { splitService } from '$lib/models/runservices';
	import type { PlanChange } from '$lib/types/deploy';
	import type { RunValueChange } from '$lib/types/runs';

	// A plan's rows, one per line the way the CLI prints them: services,
	// then values (names only, never plaintext; a run's also say which
	// services use them), then the applications a run restarts. Shared by
	// the promote and redeploy previews and by a run's recorded changes.
	// `class` sizes the box (a modal caps its height).
	let {
		changes = [],
		values = [],
		restarts = [],
		class: className = ''
	}: {
		changes?: PlanChange[];
		values?: RunValueChange[];
		restarts?: string[];
		class?: string;
	} = $props();

	function actionClass(action: string, destructive?: boolean): string {
		if (destructive || action === 'remove' || action === 'prune') return 'text-status-danger';
		if (action === 'create') return 'text-status-success';
		if (action === 'restart') return 'text-status-warning';
		return 'text-text-tertiary';
	}
</script>

<div
	class="border-border-subtle divide-border-subtle divide-y overflow-y-auto rounded-xl border {className}"
>
	{#each changes as change, i (`c${i}`)}
		<div class="flex items-center gap-2.5 px-3 py-1.75 font-mono text-sm">
			<span class="w-14 flex-none {actionClass(change.action, change.destructive)}">
				{change.action}
			</span>
			<span class="text-text-primary">{change.service}</span>
			{#if change.detail}
				<span class="text-text-faint min-w-0 truncate" title={change.detail}>
					{change.detail}
				</span>
			{/if}
			{#if change.destructive}
				<span class="text-status-danger ml-auto flex-none text-xs">destructive</span>
			{/if}
		</div>
	{/each}
	{#each values as value, i (`v${i}`)}
		<div class="flex items-center gap-2.5 px-3 py-1.75 font-mono text-sm">
			<span class="w-14 flex-none {actionClass(value.action)}">{value.action}</span>
			<span class="text-text-primary">{value.name}</span>
			<span class="text-text-faint">value</span>
			{#if value.services?.length}
				{@const users = value.services.map((s) => splitService(s).name).join(', ')}
				<span class="text-text-ghost min-w-0 truncate" title="used by {users}">
					used by {users}
				</span>
			{/if}
		</div>
	{/each}
	{#each restarts as service (service)}
		<div class="flex items-center gap-2.5 px-3 py-1.75 font-mono text-sm">
			<span class="w-14 flex-none {actionClass('restart')}">restart</span>
			<span class="text-text-primary">{service}</span>
		</div>
	{/each}
</div>
