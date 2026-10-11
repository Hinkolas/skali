<script lang="ts">
	import type { SummaryEnvironment } from '$lib/types/project';
	import Pill from '$lib/components/ui/Pill.svelte';

	// The project listings' environment rollup: the first environment and a
	// +N for the rest, so a project with many environments stays one line.
	// Names are neutral whatever they say; a configured high priority gets
	// the switcher's `high` pill. Pass projectEnvironmentRollup's order, so
	// a high-priority environment always leads.
	let { environments }: { environments: SummaryEnvironment[] } = $props();

	const [first, ...rest] = $derived(environments);
	const restTitle = $derived(
		rest.map((e) => (e.priority === 'high' ? `${e.name} (high priority)` : e.name)).join(', ')
	);
</script>

{#if first}
	<Pill text={first.name} />
	{#if first.priority === 'high'}
		<Pill text="high" tone="warning" title="high priority" />
	{/if}
	{#if rest.length > 0}
		<Pill text="+{rest.length}" title={restTitle} />
	{/if}
{/if}
