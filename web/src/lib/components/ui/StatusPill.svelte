<script lang="ts">
	import { HEALTH_META, STATUS_META, type ServiceStatus } from '$lib/service-types';
	import type { ServiceHealth } from '$lib/types/project';
	import StatusDot from './StatusDot.svelte';

	// Inline form: dot + colored label. Pill form: adds a tinted rounded
	// background (page headers next to the service title). Accepts both the
	// real health vocabulary and the legacy graph statuses.
	let {
		status,
		pill = false
	}: {
		status: ServiceStatus | ServiceHealth;
		pill?: boolean;
	} = $props();

	const meta = $derived(
		status in STATUS_META
			? STATUS_META[status as ServiceStatus]
			: HEALTH_META[status as ServiceHealth]
	);
	const pillBg: Record<string, string> = {
		'text-status-success': 'bg-status-success/10',
		'text-status-warning': 'bg-status-warning/10',
		'text-status-danger': 'bg-status-danger/10',
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
