<script lang="ts">
	import { formatBytes } from '$lib/format';
	import { renderExpression } from '$lib/types/definition';
	import Card from '$lib/components/ui/Card.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const service = $derived(data.service);

	// Read-only rows synthesized from the compiled definition; config is
	// manifest-owned, so there is nothing to edit here.
	const sections = $derived.by((): { title: string; rows: { k: string; v: string }[] }[] => {
		switch (service.type) {
			case 'application': {
				const c = service.config;
				const source =
					c.source.kind === 'image'
						? [{ k: 'Image', v: renderExpression(c.source.image) }]
						: [
								{ k: 'Build context', v: c.source.build?.context ?? '.' },
								{ k: 'Dockerfile', v: c.source.build?.dockerfile ?? 'Dockerfile' }
							];
				return [
					{
						title: 'Source',
						rows: [
							...source,
							{ k: 'Command', v: c.command?.map(renderExpression).join(' ') || 'image default' }
						]
					},
					{
						title: 'Network',
						rows: [
							...Object.entries(c.ports ?? {}).map(([name, p]) => ({
								k: `Port ${name}`,
								v: `${p.port}/${p.protocol}`
							})),
							...Object.entries(c.routes ?? {}).map(([name, r]) => ({
								k: `Route ${name}`,
								v: `${renderExpression(r.domain)}${renderExpression(r.path)}`
							}))
						]
					},
					{
						title: 'Runtime',
						rows: [
							{
								k: 'Scaling',
								v: `${c.scaling.minReplicas}-${c.scaling.maxReplicas} replicas${
									c.scaling.cpuTargetUtilization
										? ` · ${c.scaling.cpuTargetUtilization}% cpu target`
										: ''
								}`
							},
							...(c.resources?.requests?.memoryBytes
								? [{ k: 'Memory request', v: formatBytes(c.resources.requests.memoryBytes) }]
								: []),
							...(c.resources?.limits?.memoryBytes
								? [{ k: 'Memory limit', v: formatBytes(c.resources.limits.memoryBytes) }]
								: []),
							...Object.entries(c.volumes ?? {}).map(([name, v]) => ({
								k: `Volume ${name}`,
								v: `${renderExpression(v.mountPath)} · ${formatBytes(v.sizeBytes)}`
							}))
						]
					},
					{
						title: 'Environment',
						rows: Object.entries(c.environment ?? {}).map(([name, expr]) => ({
							k: name,
							v: renderExpression(expr)
						}))
					}
				];
			}
			case 'database': {
				const c = service.config;
				return [
					{
						title: 'Claim',
						rows: [
							{ k: 'Engine', v: `${c.engine} ${c.version}` },
							{ k: 'Isolation', v: c.isolation },
							{ k: 'Availability', v: c.availability },
							{ k: 'Storage', v: c.storageBytes ? formatBytes(c.storageBytes) : 'default' },
							...(c.extensions?.length ? [{ k: 'Extensions', v: c.extensions.join(', ') }] : [])
						]
					}
				];
			}
			case 'bucket': {
				const c = service.config;
				return [
					{
						title: 'Claim',
						rows: [
							{ k: 'Visibility', v: c.visibility },
							{ k: 'Versioning', v: c.versioning },
							{
								k: 'Storage quota',
								v: c.storageQuotaBytes ? formatBytes(c.storageQuotaBytes) : 'none'
							},
							...(c.objectQuota ? [{ k: 'Object quota', v: String(c.objectQuota) }] : []),
							...(c.maxObjectSizeBytes
								? [{ k: 'Max object size', v: formatBytes(c.maxObjectSizeBytes) }]
								: [])
						]
					}
				];
			}
		}
	});
</script>

<svelte:head>
	<title>Settings · {service.name} — skali</title>
</svelte:head>

<div class="mb-3.5 flex items-baseline gap-2.5">
	<h2 class="text-text-primary text-xl font-semibold">Configuration</h2>
	<div class="text-text-ghost text-md">
		read-only · edit skali.yaml and deploy to change any of this
	</div>
</div>

<div class="flex max-w-3xl flex-col gap-3.5 pb-6">
	{#each sections as section (section.title)}
		{#if section.rows.length > 0}
			<Card class="p-5">
				<h3 class="text-text-primary mb-2 text-xl font-semibold">{section.title}</h3>
				<div class="flex flex-col">
					{#each section.rows as row (row.k)}
						<KeyValueRow k={row.k} v={row.v} />
					{/each}
				</div>
			</Card>
		{/if}
	{/each}
</div>
