<script lang="ts">
	import type { ServiceStatus } from '$lib/mock/types';
	import { STATUS_META } from '$lib/service-types';
	import StatusDot from './StatusDot.svelte';

	// Inline form: dot + colored label. Pill form: adds a tinted rounded
	// background (page headers next to the service title).
	let {
		status,
		pill = false
	}: {
		status: ServiceStatus;
		pill?: boolean;
	} = $props();

	const meta = $derived(STATUS_META[status]);
	const pillBg: Record<string, string> = {
		'text-status-success': 'bg-status-success/10',
		'text-status-warning': 'bg-status-warning/10',
		'text-text-muted': 'bg-white/6'
	};
</script>

{#if pill}
	<span
		class="flex items-center gap-1.5 rounded-full px-2.75 py-1 text-md {meta.text} {pillBg[
			meta.text
		]}"
	>
		<StatusDot {status} />{meta.label}
	</span>
{:else}
	<span class="flex items-center gap-1.5 text-md {meta.text}">
		<StatusDot {status} />{meta.label}
	</span>
{/if}
