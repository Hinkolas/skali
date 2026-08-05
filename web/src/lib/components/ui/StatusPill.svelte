<script lang="ts">
	import { HEALTH_META, STATUS_META, type ServiceStatus } from '$lib/service-types';
	import type { ServiceHealth } from '$lib/types/project';
	import type { HealthDiagnostic } from '$lib/types/status';
	import StatusDot from './StatusDot.svelte';
	import Tooltip from './Tooltip.svelte';

	// Inline form: dot + colored label. Pill form: adds a tinted rounded
	// background (page headers next to the service title). Accepts both the
	// real health vocabulary and the legacy graph statuses.
	//
	// Health alone says a service is not settled but never why: the reason
	// lives in the status projection's diagnostics (a progressing database is
	// claim-provisioning, tenant-unobserved, pool-unobserved, and so on). Pass
	// them and the pill explains itself on hover instead of sending the reader
	// to the API.
	let {
		status,
		pill = false,
		diagnostics = [],
		align = 'start',
		focusable = true
	}: {
		status: ServiceStatus | ServiceHealth;
		pill?: boolean;
		/** Diagnostics behind this status, empty when there is nothing to explain. */
		diagnostics?: HealthDiagnostic[];
		/** Tooltip edge alignment: `end` for a pill sitting on a card's right edge. */
		align?: 'start' | 'end';
		/** False inside an interactive ancestor, where a nested tab stop is invalid. */
		focusable?: boolean;
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
	const severityText: Record<string, string> = {
		error: 'text-status-danger',
		warning: 'text-status-warning',
		info: 'text-text-faint'
	};
	const explained = $derived(diagnostics.length > 0);
</script>

{#snippet badge()}
	{#if pill}
		<span
			class="flex items-center gap-1.5 rounded-full px-2.75 py-1 text-md {meta.text} {pillBg[
				meta.text
			]} {explained ? 'cursor-help' : ''}"
		>
			<StatusDot {status} />{meta.label}
		</span>
	{:else}
		<span class="flex items-center gap-1.5 text-md {meta.text} {explained ? 'cursor-help' : ''}">
			<StatusDot {status} />{meta.label}
		</span>
	{/if}
{/snippet}

{#if explained}
	<Tooltip {align} {focusable} trigger={badge}>
		<div class="flex flex-col gap-2.5">
			{#each diagnostics as diagnostic (diagnostic.code + (diagnostic.resource ?? ''))}
				<div class="flex flex-col gap-0.5">
					<div class="font-mono text-xs {severityText[diagnostic.severity] ?? 'text-text-faint'}">
						{diagnostic.code}
					</div>
					<p class="text-text-secondary text-md leading-snug">{diagnostic.message}</p>
					{#if diagnostic.resource}
						<div class="font-mono text-text-ghost text-xs">{diagnostic.resource}</div>
					{/if}
				</div>
			{/each}
		</div>
	</Tooltip>
{:else}
	{@render badge()}
{/if}
