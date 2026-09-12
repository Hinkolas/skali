<script lang="ts">
	import { page } from '$app/state';
	import type { EnvironmentStatus, PodStatus } from '$lib/types/status';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { relativeTime } from '$lib/format';
	import Pill from '$lib/components/ui/Pill.svelte';

	// The pods of one service from the live status projection: their node,
	// restarts, age, and on blue-green workloads which color is serving.
	// Serving pods sort first, then by name, so the active color reads as one
	// block during a switch.
	let { pods: input }: { pods: PodStatus[] } = $props();

	const pods = $derived(
		input.toSorted((a, b) => Number(b.serving) - Number(a.serving) || a.name.localeCompare(b.name))
	);
	const observed = $derived(
		((envStatus.doc ?? (page.data.status as EnvironmentStatus | null))?.observation.state ??
			'unknown') === 'fresh'
	);

	// A pod that is not ready is usually just starting (ContainerCreating,
	// PodInitializing); only the reasons that mean it will not get there on
	// its own read as failures.
	const FAILING = new Set([
		'CrashLoopBackOff',
		'Error',
		'ErrImagePull',
		'ImagePullBackOff',
		'OOMKilled',
		'CreateContainerConfigError'
	]);
	function podDot(pod: PodStatus): string {
		if (pod.ready) return 'bg-status-success';
		if (pod.phase === 'Failed' || (pod.reason && FAILING.has(pod.reason)))
			return 'bg-status-danger';
		return 'bg-status-warning';
	}
	function podState(pod: PodStatus): string {
		if (pod.ready) return 'ready';
		return (pod.reason ?? pod.phase).toLowerCase();
	}
</script>

{#if pods.length > 0}
	<div class="border-border-default overflow-hidden rounded-[11px] border">
		{#each pods as pod (pod.name)}
			<div
				class="border-border-subtle grid grid-cols-[minmax(0,1.8fr)_minmax(0,1fr)_auto_auto_auto] items-center gap-3 border-b px-3 py-2.25 last:border-0"
			>
				<div class="flex min-w-0 items-center gap-2">
					<span class="size-[8px] flex-none rounded-full {podDot(pod)}" title={podState(pod)}
					></span>
					<span class="text-text-primary truncate font-mono text-sm">{pod.name}</span>
				</div>
				<span class="text-text-faint truncate font-mono text-xs" title="node">{pod.node}</span>
				<span
					class="font-mono text-xs {pod.restarts > 0 ? 'text-status-warning' : 'text-text-faint'}"
					title="restarts"
				>
					{pod.restarts} restart{pod.restarts === 1 ? '' : 's'}
				</span>
				<span class="text-text-faint font-mono text-xs" title="started">
					{relativeTime(pod.started_at)}
				</span>
				<span class="flex w-16 justify-end">
					{#if pod.color}
						<Pill text={pod.color} tone={pod.serving ? 'success' : 'neutral'} />
					{:else if pod.serving}
						<Pill text="serving" tone="success" />
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
				{observed ? 'No pods running' : 'No pods observed'}
			</div>
			<div class="font-mono text-text-faint text-md">
				{observed ? 'the workload has no replicas right now' : 'the cluster is not observed'}
			</div>
		</div>
	</div>
{/if}
