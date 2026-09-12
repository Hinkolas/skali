<script lang="ts">
	import type { DatabaseView } from '$lib/models/service';
	import type { DatabaseConnection } from '$lib/types/connections';
	import { envStatus } from '$lib/stores/envstatus.svelte';
	import { HEALTH_META } from '$lib/service-types';
	import { formatBytes } from '$lib/format';
	import { describeSeconds } from '$lib/cron';
	import Card from '$lib/components/ui/Card.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import PodList from './PodList.svelte';

	// The server behind one database claim: the engine it runs on, how it is
	// isolated and replicated, and what the claim asked for. Dedicated
	// databases own their pods and list them; shared ones live in the
	// platform's pool, which has no per-tenant pods to show.
	let { service, connection }: { service: DatabaseView; connection: DatabaseConnection | null } =
		$props();

	const live = $derived(envStatus.service('database', service.key));
	const health = $derived(live?.health ?? 'unknown');
	const meta = $derived(HEALTH_META[health]);
	const config = $derived(service.config);

	const engine = $derived(
		connection ? `${config.engine} ${connection.major}` : `${config.engine} ${config.version}`
	);
	const ISOLATION: Record<string, string> = {
		shared: 'shared · platform pool',
		project: 'project · one cluster per project',
		dedicated: 'dedicated · own cluster'
	};
	const isolation = $derived(ISOLATION[config.isolation] ?? config.isolation);
	const storage = $derived(config.storageBytes ? formatBytes(config.storageBytes) : 'default');
	const extensions = $derived(config.extensions?.join(', ') || 'none');
	const recovery = $derived(
		config.pointInTimeRecoverySeconds
			? `${describeSeconds(config.pointInTimeRecoverySeconds)} window`
			: 'snapshots only'
	);

	const pods = $derived(live?.pods ?? []);
	const dedicated = $derived(config.isolation !== 'shared');
</script>

<Card class="flex flex-col p-5">
	<div class="mb-3.5 flex items-center gap-2.5">
		<h3 class="text-text-primary text-xl font-semibold">Instance</h3>
		<span class="flex items-center gap-1.5 text-md {meta.text}">
			<span class="size-[8px] rounded-full {meta.dot}"></span>{meta.label.toLowerCase()}
		</span>
	</div>
	<div class="flex flex-col">
		<KeyValueRow k="Engine" v={engine} labelWidth="w-36" />
		<KeyValueRow k="Isolation" v={isolation} labelWidth="w-36" />
		<KeyValueRow k="Availability" v={config.availability} labelWidth="w-36" />
		<KeyValueRow k="Storage requested" v={storage} labelWidth="w-36" />
		<KeyValueRow k="Extensions" v={extensions} labelWidth="w-36" />
		<KeyValueRow k="Point-in-time recovery" v={recovery} labelWidth="w-36" />
	</div>

	{#if dedicated || pods.length > 0}
		<div class="mt-5 mb-2.5 flex items-baseline gap-2.5">
			<h4 class="text-text-primary text-base font-semibold">Pods</h4>
			<span class="font-mono text-text-faint text-xs">
				{pods.filter((p) => p.ready).length}/{pods.length} ready
			</span>
		</div>
		<PodList {pods} />
	{/if}
</Card>
