<script lang="ts">
	import type { ApplicationView } from '$lib/models/service';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { HEALTH_META } from '$lib/service-types';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import PodList from './PodList.svelte';

	// The running process of one application: how it is built and started
	// (from the definition) and the pods that currently run it (from the live
	// status projection).
	let { service }: { service: ApplicationView } = $props();

	const live = $derived(envStatus.service('application', service.key));
	const health = $derived(live?.health ?? 'unknown');
	const meta = $derived(HEALTH_META[health]);

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

	const pods = $derived(live?.pods ?? []);
	const readyPods = $derived(pods.filter((p) => p.ready).length);
	const scaling = $derived(service.config.scaling);
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
	<PodList pods={live?.pods ?? []} />
</Card>
