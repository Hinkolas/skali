<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import Lock from '@lucide/svelte/icons/lock';
	import type { Expression } from '$lib/types/definition';
	import { SERVICE_KIND_META } from '$lib/service-types';
	import { withEnv } from '$lib/urls';

	// A compiled expression rendered part by part: literals as text, project
	// values as accent chips, service outputs as chips in the service's
	// color that link to the service. Sensitive outputs carry a lock.
	let { expression }: { expression: Expression | undefined } = $props();

	const projectName = $derived((page.data.project as { name: string }).name);
	const env = $derived((page.data.env as { name: string } | null)?.name ?? null);

	const kindOf = (collection: string | undefined) =>
		collection === 'databases' ? 'database' : 'bucket';
</script>

<span class="inline-flex flex-wrap items-center gap-1 font-mono text-md">
	{#each expression?.parts ?? [] as part, i (i)}
		{#if part.kind === 'literal'}
			<span class="text-text-secondary">{part.value ?? ''}</span>
		{:else if part.kind === 'project_variable'}
			<span
				class="bg-accent/12 text-accent-light rounded-[6px] px-1.5 py-0.5 text-xs"
				title={part.hasDefault
					? `project value ${part.name}, default "${part.default ?? ''}"`
					: `project value ${part.name}`}
			>
				$&#123;{part.name}&#125;
			</span>
		{:else if part.kind === 'service_output'}
			{@const kind = kindOf(part.collection)}
			{@const meta = SERVICE_KIND_META[kind]}
			<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
			<a
				href={withEnv(
					resolve('/(app)/projects/[project]/services/[service]', {
						project: projectName,
						service: part.service ?? ''
					}),
					env
				)}
				class="inline-flex items-center gap-1 rounded-[6px] px-1.5 py-0.5 text-xs hover:underline {meta.text} {meta.bg}"
				title="{part.output} of {part.collection}.{part.service}{part.sensitive
					? ' · sensitive, never shown'
					: ''}"
			>
				<meta.icon size={11} />
				{part.service}.{part.output}
				{#if part.sensitive}
					<Lock size={10} aria-label="sensitive" />
				{/if}
			</a>
			<!-- eslint-enable svelte/no-navigation-without-resolve -->
		{:else}
			<span class="text-text-faint">$&#123;{part.name ?? part.kind}&#125;</span>
		{/if}
	{:else}
		<span class="text-text-ghost">empty</span>
	{/each}
</span>
