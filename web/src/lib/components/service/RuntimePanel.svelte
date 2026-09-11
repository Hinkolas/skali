<script lang="ts">
	import { page } from '$app/state';
	import type { ApplicationView } from '$lib/models/service';
	import type { EnvironmentStatus, PodStatus } from '$lib/types/status';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { HEALTH_META } from '$lib/service-types';
	import { relativeTime } from '$lib/format';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';

	// The running process of one application: how it is built and started
	// (from the definition) and the pods that currently run it (from the live
	// status projection). The pod list is the one place the console shows
	// individual replicas: their node, restarts, age, and on blue-green
	// workloads which color is serving.
	let { service }: { service: ApplicationView } = $props();

	const live = $derived(envStatus.service('application', service.key));
	const health = $derived(live?.health ?? 'unknown');
	const meta = $derived(HEALTH_META[health]);
	const observed = $derived(
		((envStatus.doc ?? (page.data.status as EnvironmentStatus | null))?.observation.state ??
			'unknown') === 'fresh'
	);

	const source = $derived(
		service.config.source.kind === 'image'
			? service.config.source.image || 'image'
			: `build ${service.config.source.build?.context ?? '.'}`
	);
	const command = $derived(service.config.command?.join(' ') || 'image default');
	const ports = $derived(
		Object.entries(service.config.ports ?? {})
			.map(([name, p]) => `${name} ${p.port}/${p.protocol}`)
			.join(' · ') || 'none'
	);
	const healthCheck = $derived.by(() => {
		const probe = service.config.health?.readiness ?? service.config.health?.liveness;
		return probe?.http ? `http ${probe.http.path}` : 'none configured';
	});
	const strategy = $derived(service.config.deployment?.rollout?.strategy ?? 'blue-green');

	// Serving pods first, then by name, so the active color reads as one
	// block during a blue-green switch.
	const pods = $derived(
		(live?.pods ?? []).toSorted(
			(a, b) => Number(b.serving) - Number(a.serving) || a.name.localeCompare(b.name)
		)
	);
	const readyPods = $derived(pods.filter((p) => p.ready).length);
	const scaling = $derived(service.config.scaling);

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

<Card class="flex flex-col p-5">
	<div class="mb-3.5 flex items-center gap-2.5">
		<h3 class="text-text-primary text-xl font-semibold">Runtime</h3>
		<span class="flex items-center gap-1.5 text-md {meta.text}">
			<span class="size-[8px] rounded-full {meta.dot}"></span>{meta.label.toLowerCase()}
		</span>
		<div class="ml-auto flex gap-2">
			<span title="Shell access is coming soon">
				<Button size="sm" disabled>Shell</Button>
			</span>
		</div>
	</div>
	<div class="flex flex-col">
		<KeyValueRow k="Source" v={source} />
		<KeyValueRow k="Start command" v={command} />
		<KeyValueRow k="Ports" v={ports} />
		<KeyValueRow k="Health check" v={healthCheck} />
		<KeyValueRow k="Rollout" v={strategy} />
	</div>

	<div class="mt-5 mb-2.5 flex items-baseline gap-2.5">
		<h4 class="text-text-primary text-base font-semibold">Pods</h4>
		<span class="font-mono text-text-faint text-xs">
			{readyPods}/{pods.length} ready · scale {scaling.minReplicas}-{scaling.maxReplicas}
		</span>
	</div>
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
</Card>
