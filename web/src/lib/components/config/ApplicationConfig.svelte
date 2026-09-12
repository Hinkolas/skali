<script lang="ts">
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import Container from '@lucide/svelte/icons/container';
	import FileCode from '@lucide/svelte/icons/file-code';
	import FolderOpen from '@lucide/svelte/icons/folder-open';
	import type { ApplicationView } from '$lib/models/service';
	import type { Probe } from '$lib/types/definition';
	import { formatBytes, formatCores, formatMillis } from '$lib/format';
	import Pill from '$lib/components/ui/Pill.svelte';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import Choice from './Choice.svelte';
	import CodeBlock from './CodeBlock.svelte';
	import ConfigCard from './ConfigCard.svelte';
	import ExpressionValue from './ExpressionValue.svelte';
	import Meter from './Meter.svelte';

	// The compiled application, one card per concern, each drawn the way
	// its concern is thought about: the build as a pipeline, resources as
	// request-in-limit bars, scaling as a replica range, the rollout as the
	// strategy among its alternatives, and every environment variable with
	// the exact source of each of its parts.
	let { service }: { service: ApplicationView } = $props();

	const c = $derived(service.config);

	// An image reference split into registry, repository, and tag or digest.
	const image = $derived.by(() => {
		const ref = c.source.image ?? '';
		const firstSlash = ref.indexOf('/');
		const head = firstSlash > 0 ? ref.slice(0, firstSlash) : '';
		const registry = head.includes('.') || head.includes(':') || head === 'localhost' ? head : '';
		let rest = registry ? ref.slice(firstSlash + 1) : ref;
		let digest = '';
		let tag = '';
		const at = rest.indexOf('@');
		if (at >= 0) {
			digest = rest.slice(at + 1);
			rest = rest.slice(0, at);
		}
		const colon = rest.lastIndexOf(':');
		if (colon > rest.lastIndexOf('/')) {
			tag = rest.slice(colon + 1);
			rest = rest.slice(0, colon);
		}
		return {
			registry: registry || 'docker.io',
			repository: rest,
			tag: tag || (digest ? '' : 'latest'),
			digest
		};
	});

	const ports = $derived(Object.entries(c.ports ?? {}));
	const routes = $derived(Object.entries(c.routes ?? {}));
	const volumes = $derived(Object.entries(c.volumes ?? {}));
	const environment = $derived(
		Object.entries(c.environment ?? {}).toSorted(([a], [b]) => a.localeCompare(b))
	);
	const buildArguments = $derived(Object.entries(c.source.build?.arguments ?? {}));

	const scaling = $derived(c.scaling);
	const fixed = $derived(scaling.minReplicas === scaling.maxReplicas);
	// Ticks for one replica each, up to a dozen; beyond that the band alone
	// carries the range.
	const replicaTicks = $derived(
		scaling.maxReplicas <= 12 ? Array.from({ length: scaling.maxReplicas }, (_, i) => i + 1) : []
	);

	const PROBES: { key: 'startup' | 'readiness' | 'liveness'; label: string; purpose: string }[] = [
		{ key: 'startup', label: 'Startup', purpose: 'holds the other probes until the app is up' },
		{ key: 'readiness', label: 'Readiness', purpose: 'gates traffic to the pod' },
		{ key: 'liveness', label: 'Liveness', purpose: 'restarts a pod that stops answering' }
	];
	const portLabel = (target: { name?: string; number?: number } | undefined) =>
		target?.name ?? (target?.number != null ? `${target.number}` : 'default');
	const probeTarget = (probe: Probe) => (probe.http ? `GET ${probe.http.path}` : 'tcp connect');

	const STRATEGIES = [
		{
			value: 'blue-green',
			description: 'the new color comes up beside the old, traffic switches at once'
		},
		{ value: 'rolling', description: 'pods are replaced a few at a time' },
		{ value: 'recreate', description: 'everything stops, then the new revision starts' }
	];
	const rollout = $derived(c.deployment?.rollout);
	const strategy = $derived(rollout?.strategy ?? 'blue-green');
	const release = $derived(c.deployment?.releaseCommand);
	const placement = $derived(c.placement);
</script>

<div class="grid grid-cols-2 gap-3.5 pb-6">
	<ConfigCard
		title="Source"
		hint={c.source.kind === 'image' ? 'a published image' : 'built from the repository'}
	>
		{#if c.source.kind === 'image'}
			<div class="flex flex-wrap items-center gap-1.5 font-mono text-md">
				<span class="bg-white/6 text-text-muted rounded-[6px] px-1.5 py-0.5">{image.registry}</span>
				<span class="text-text-ghost">/</span>
				<span class="text-text-primary">{image.repository}</span>
				{#if image.tag}
					<span class="text-text-ghost">:</span>
					<span class="bg-accent/12 text-accent-light rounded-[6px] px-1.5 py-0.5">{image.tag}</span
					>
				{/if}
				{#if image.digest}
					<span class="text-text-ghost">@</span>
					<span class="text-text-faint truncate text-xs" title={image.digest}>
						{image.digest.slice(0, 19)}…
					</span>
				{/if}
			</div>
		{:else}
			<div class="flex items-center gap-2 font-mono text-md">
				<span
					class="border-border-default flex min-w-0 items-center gap-2 rounded-[9px] border px-2.5 py-2"
					title="build context"
				>
					<FolderOpen size={14} class="text-text-faint flex-none" />
					<span class="text-text-primary truncate">{c.source.build?.context ?? '.'}</span>
				</span>
				<ArrowRight size={14} class="text-text-ghost flex-none" />
				<span
					class="border-border-default flex min-w-0 items-center gap-2 rounded-[9px] border px-2.5 py-2"
					title="Dockerfile"
				>
					<FileCode size={14} class="text-text-faint flex-none" />
					<span class="text-text-primary truncate"
						>{c.source.build?.dockerfile ?? 'Dockerfile'}</span
					>
					{#if c.source.build?.target}
						<Pill text="target {c.source.build.target}" />
					{/if}
				</span>
				<ArrowRight size={14} class="text-text-ghost flex-none" />
				<span
					class="border-border-default flex items-center gap-2 rounded-[9px] border px-2.5 py-2"
					title="the image skali builds and pushes to its registry"
				>
					<Container size={14} class="text-service-app flex-none" />
					<span class="text-text-muted">image</span>
				</span>
			</div>
			{#if buildArguments.length > 0}
				<div class="mt-3 flex flex-col">
					{#each buildArguments as [name, value] (name)}
						<div
							class="border-border-subtle flex items-baseline gap-3 border-b py-1.5 last:border-0"
						>
							<span class="text-text-faint text-md">arg</span>
							<span class="text-text-primary font-mono text-md">{name}</span>
							<span class="text-text-secondary ml-auto truncate font-mono text-md">{value}</span>
						</div>
					{/each}
				</div>
			{/if}
		{/if}
		{#if c.source.platforms?.length}
			<div class="mt-3 flex items-center gap-1.5">
				<span class="text-text-faint text-md">platforms</span>
				{#each c.source.platforms as platform (platform)}
					<Pill text={platform} />
				{/each}
			</div>
		{/if}
		<div class="mt-4">
			<div class="text-text-tertiary mb-1.5 text-md font-medium">Start command</div>
			<CodeBlock command={c.command?.join(' ') || 'image default'} muted={!c.command?.length} />
		</div>
	</ConfigCard>

	<ConfigCard
		title="Network"
		hint={routes.length > 0 ? 'reachable from the internet' : 'private to the cluster'}
	>
		<div class="flex flex-col gap-4">
			<div>
				<div class="text-text-tertiary mb-1.5 text-md font-medium">Ports</div>
				{#if ports.length > 0}
					<div class="flex flex-wrap gap-2">
						{#each ports as [name, port] (name)}
							<span
								class="border-border-default flex items-baseline gap-2 rounded-[9px] border px-2.5 py-2 font-mono"
							>
								<span class="text-text-primary text-md">{name}</span>
								<span class="text-text-muted text-md">{port.port}</span>
								<span class="text-text-faint text-xs uppercase">{port.protocol}</span>
							</span>
						{/each}
					</div>
				{:else}
					<div class="text-text-faint font-mono text-md">none · nothing listens for traffic</div>
				{/if}
			</div>
			<div>
				<div class="text-text-tertiary mb-1.5 text-md font-medium">Routes</div>
				{#if routes.length > 0}
					<div class="flex flex-col">
						{#each routes as [name, route] (name)}
							<div
								class="border-border-subtle flex flex-wrap items-center gap-x-3 gap-y-1.5 border-b py-2 last:border-0"
							>
								<span class="text-text-faint w-16 flex-none font-mono text-xs">{name}</span>
								<span class="flex min-w-0 items-center gap-0.5">
									<ExpressionValue expression={route.domain} />
									<span class="text-text-secondary font-mono text-md">{route.path}</span>
								</span>
								<ArrowRight size={13} class="text-text-ghost flex-none" />
								<span class="text-text-muted font-mono text-md">port {portLabel(route.port)}</span>
								<span class="ml-auto flex items-center gap-1.5">
									<Pill
										text="tls {route.tls}"
										tone={route.tls === 'automatic'
											? 'success'
											: route.tls === 'disabled'
												? 'warning'
												: 'neutral'}
									/>
								</span>
							</div>
						{/each}
					</div>
				{:else}
					<div class="text-text-faint font-mono text-md">none · no public domain</div>
				{/if}
			</div>
		</div>
	</ConfigCard>

	<ConfigCard
		title="Scaling"
		hint={fixed
			? `fixed at ${scaling.minReplicas} replica${scaling.minReplicas === 1 ? '' : 's'}`
			: `${scaling.minReplicas} to ${scaling.maxReplicas} replicas`}
	>
		<div class="flex flex-col gap-4">
			<div>
				<div class="relative h-7">
					<div class="absolute inset-x-0 top-3 h-1.5 rounded-full bg-white/6"></div>
					<div
						class="bg-accent/60 absolute top-3 h-1.5 rounded-full"
						style:left="{((scaling.minReplicas - 1) / Math.max(1, scaling.maxReplicas - 1)) * 100}%"
						style:right="0%"
					></div>
					{#each replicaTicks as n (n)}
						<span
							class="absolute top-2 h-3.5 w-px -translate-x-1/2 {n >= scaling.minReplicas
								? 'bg-accent-light'
								: 'bg-white/15'}"
							style:left="{((n - 1) / Math.max(1, scaling.maxReplicas - 1)) * 100}%"
						></span>
					{/each}
				</div>
				<div class="text-text-faint flex justify-between font-mono text-xs">
					<span>1</span>
					{#if !fixed}
						<span class="text-text-primary">min {scaling.minReplicas}</span>
					{/if}
					<span class="text-text-primary">max {scaling.maxReplicas}</span>
				</div>
			</div>
			<div>
				<div class="mb-1.5 flex items-baseline">
					<span class="text-text-tertiary text-md font-medium">CPU target</span>
					<span class="text-text-faint ml-auto font-mono text-xs">
						{#if scaling.cpuTargetUtilization && !fixed}
							scales out above <span class="text-text-primary">{scaling.cpuTargetUtilization}%</span
							>
							average CPU
						{:else}
							no autoscaling
						{/if}
					</span>
				</div>
				<ProgressBar
					pct={scaling.cpuTargetUtilization && !fixed ? scaling.cpuTargetUtilization : 0}
					class="bg-chart-1"
				/>
			</div>
		</div>
	</ConfigCard>

	<ConfigCard title="Resources" hint="per replica">
		<div class="flex flex-col gap-3.5">
			<Meter
				label="CPU"
				request={c.resources?.requests?.milliCpu}
				limit={c.resources?.limits?.milliCpu}
				format={formatCores}
				class="bg-chart-1"
			/>
			<Meter
				label="Memory"
				request={c.resources?.requests?.memoryBytes}
				limit={c.resources?.limits?.memoryBytes}
				format={formatBytes}
				class="bg-chart-2"
			/>
			<Meter
				label="Temporary storage"
				request={c.resources?.requests?.temporaryStorageBytes}
				limit={c.resources?.limits?.temporaryStorageBytes}
				format={formatBytes}
				class="bg-service-cache"
			/>
		</div>
	</ConfigCard>

	<ConfigCard
		title="Health checks"
		hint="how the platform decides a pod is well"
		class="col-span-2"
	>
		<div class="grid grid-cols-3 gap-3">
			{#each PROBES as probe (probe.key)}
				{@const declared = c.health?.[probe.key]}
				<div
					class="flex flex-col gap-2 rounded-[11px] border px-3.5 py-3 {declared
						? 'border-border-default'
						: 'border-border-default border-dashed opacity-60'}"
				>
					<div class="flex items-baseline gap-2">
						<span class="text-text-primary text-base font-medium">{probe.label}</span>
						<span class="text-text-faint text-xs">{probe.purpose}</span>
					</div>
					{#if declared}
						<div class="text-text-secondary font-mono text-md">
							{probeTarget(declared)}
							<span class="text-text-faint">· port {portLabel(declared.http?.port)}</span>
						</div>
						<div class="text-text-faint flex flex-wrap gap-x-3 font-mono text-xs">
							<span
								>every <span class="text-text-secondary"
									>{formatMillis(declared.intervalMillis ?? 10_000)}</span
								></span
							>
							<span
								>timeout <span class="text-text-secondary"
									>{formatMillis(declared.timeoutMillis ?? 1000)}</span
								></span
							>
							<span
								>fails after <span class="text-text-secondary"
									>{declared.failureThreshold ?? 3}</span
								></span
							>
						</div>
					{:else}
						<div class="text-text-faint font-mono text-md">not configured</div>
					{/if}
				</div>
			{/each}
		</div>
	</ConfigCard>

	<ConfigCard title="Rollout" hint="how a new revision replaces the running one">
		<Choice options={STRATEGIES} value={strategy} label="Rollout strategy" />
		<div class="mt-3 flex flex-col">
			{#if rollout?.maxSurge != null}
				<div class="border-border-subtle flex items-baseline gap-3 border-b py-1.5 last:border-0">
					<span class="text-text-faint text-md">Extra pods during rollout</span>
					<span class="text-text-secondary ml-auto font-mono text-md">up to {rollout.maxSurge}</span
					>
				</div>
			{/if}
			{#if rollout?.maxUnavailable != null}
				<div class="border-border-subtle flex items-baseline gap-3 border-b py-1.5 last:border-0">
					<span class="text-text-faint text-md">Pods allowed down</span>
					<span class="text-text-secondary ml-auto font-mono text-md"
						>up to {rollout.maxUnavailable}</span
					>
				</div>
			{/if}
			<div class="border-border-subtle flex items-baseline gap-3 border-b py-1.5 last:border-0">
				<span class="text-text-faint text-md">Rollout timeout</span>
				<span class="text-text-secondary ml-auto font-mono text-md">
					{rollout?.timeoutMillis ? formatMillis(rollout.timeoutMillis) : 'default'}
				</span>
			</div>
			<div class="border-border-subtle flex items-baseline gap-3 border-b py-1.5 last:border-0">
				<span class="text-text-faint text-md">Shutdown grace period</span>
				<span class="text-text-secondary ml-auto font-mono text-md">
					{c.shutdown?.gracePeriodMillis ? formatMillis(c.shutdown.gracePeriodMillis) : 'default'}
				</span>
			</div>
		</div>
		<div class="mt-4">
			<div class="mb-1.5 flex items-baseline">
				<span class="text-text-tertiary text-md font-medium whitespace-nowrap">Release command</span
				>
				<span class="text-text-faint ml-auto text-right font-mono text-xs">
					{#if release?.command?.length}
						before each rollout{release.timeoutMillis
							? ` · ${formatMillis(release.timeoutMillis)} timeout`
							: ''}
					{:else}
						none
					{/if}
				</span>
			</div>
			{#if release?.command?.length}
				<CodeBlock command={release.command.join(' ')} />
			{/if}
		</div>
	</ConfigCard>

	<div class="flex flex-col gap-3.5">
		<ConfigCard
			title="Placement"
			hint={placement ? 'spread constraints' : 'none · the scheduler decides'}
			class="flex-1"
		>
			{#if placement}
				<div class="flex flex-col gap-3">
					<div class="flex items-baseline gap-3">
						<span class="text-text-faint text-md">Spread across</span>
						<span class="text-text-primary ml-auto font-mono text-md">
							{placement.spreadAcross ?? 'nodes'}{placement.minimum
								? ` · at least ${placement.minimum}`
								: ''}
						</span>
					</div>
					<Choice
						label="Enforcement"
						value={placement.enforcement ?? 'preferred'}
						options={[
							{
								value: 'preferred',
								description: 'best effort, pods still run when it cannot hold'
							},
							{ value: 'required', description: 'pods stay pending until it can hold' }
						]}
					/>
				</div>
			{:else}
				<div class="text-text-faint font-mono text-md">
					pods land wherever the scheduler prefers
				</div>
			{/if}
		</ConfigCard>
		{#if volumes.length > 0}
			<ConfigCard title="Volumes" hint="persistent storage mounted into every pod">
				<div class="flex flex-col">
					{#each volumes as [name, volume] (name)}
						<div class="border-border-subtle flex items-baseline gap-3 border-b py-2 last:border-0">
							<span class="text-text-primary font-mono text-md">{name}</span>
							<span class="text-text-muted truncate font-mono text-md">{volume.mountPath}</span>
							<span class="text-text-secondary ml-auto font-mono text-md"
								>{formatBytes(volume.sizeBytes)}</span
							>
						</div>
					{/each}
				</div>
			</ConfigCard>
		{/if}
	</div>

	<ConfigCard
		title="Environment"
		hint="{environment.length} variable{environment.length === 1
			? ''
			: 's'} · values resolve at deploy"
		class="col-span-2"
	>
		{#if environment.length > 0}
			<div class="flex flex-col">
				{#each environment as [name, expression] (name)}
					<div
						class="border-border-subtle grid grid-cols-[minmax(0,1fr)_minmax(0,2.2fr)] items-center gap-4 border-b py-2 last:border-0"
					>
						<span class="text-text-primary truncate font-mono text-md">{name}</span>
						<ExpressionValue {expression} />
					</div>
				{/each}
			</div>
		{:else}
			<div class="text-text-faint font-mono text-md">no variables declared</div>
		{/if}
	</ConfigCard>
</div>
