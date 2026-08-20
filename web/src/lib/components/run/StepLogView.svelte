<script lang="ts">
	import { api } from '$lib/api/client';
	import type { LogEntry, LogsPage } from '$lib/types/runs';
	import { openStream } from '$lib/sse';
	import { formatDateTime } from '$lib/format';

	// One step's structured log: a REST seed page establishes the cursor,
	// then (while the step is running) the step-log SSE stream appends live
	// entries, resuming from the seed's cursor.
	let { stepId, live = false }: { stepId: string; live?: boolean } = $props();

	let entries = $state<LogEntry[]>([]);
	let container = $state<HTMLDivElement | null>(null);

	$effect(() => {
		const id = stepId;
		entries = [];
		let closed = false;
		let handle: { close(): void } | null = null;

		void api.get<LogsPage>(`/v1/steps/${id}/logs`).then((page) => {
			if (closed || id !== stepId) return;
			entries = page.logs;
			if (!live) return;
			handle = openStream<LogEntry>({
				path: `/v1/steps/${id}/logs/stream`,
				events: 'log',
				after: page.next,
				onEvent: (entry) => {
					if (id !== stepId) return;
					entries = [...entries, entry];
				}
			});
		});
		return () => {
			closed = true;
			handle?.close();
		};
	});

	// Follow the tail while new entries stream in.
	$effect(() => {
		void entries.length;
		if (container) container.scrollTop = container.scrollHeight;
	});

	const levelClass: Record<string, string> = {
		error: 'text-status-danger',
		warn: 'text-status-warning',
		info: 'text-text-secondary',
		debug: 'text-text-ghost'
	};
</script>

<div
	bind:this={container}
	class="bg-surface-canvas border-border-subtle max-h-56 overflow-y-auto rounded-[9px] border px-3 py-2"
>
	{#if entries.length === 0}
		<div class="font-mono text-text-faint py-1 text-md">no log output</div>
	{:else}
		{#each entries as entry (`${entry.attempt}:${entry.seq}`)}
			<div class="flex gap-2.5 py-0.5">
				<span class="font-mono text-text-faint flex-none text-xs" title={formatDateTime(entry.ts)}>
					{new Date(entry.ts).toLocaleTimeString(undefined, { hour12: false })}
				</span>
				<span
					class="font-mono min-w-0 text-xs break-all {levelClass[entry.level] ??
						'text-text-secondary'}"
				>
					{entry.message}
				</span>
			</div>
		{/each}
	{/if}
</div>
