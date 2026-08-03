<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { fly } from 'svelte/transition';
	import X from '@lucide/svelte/icons/x';
	import type { GraphNodeData } from '$lib/mock/types';
	import { currentEnv, withEnv } from '$lib/urls';

	let {
		node,
		projectSlug,
		onclose
	}: {
		node: GraphNodeData;
		projectSlug: string;
		onclose: () => void;
	} = $props();

	// Only rendered in project scope, so page.data.project is present.
	const env = $derived(currentEnv(page.data.project, page.url));
</script>

<div
	class="bg-surface-drawer border-border-strong absolute top-3.5 right-3.5 bottom-3.5 z-5 flex w-[319px] flex-col rounded-[15px] border p-4.5 shadow-[0_8px_40px_rgba(0,0,0,0.5)]"
	transition:fly={{ x: 12, duration: 160 }}
>
	<div class="flex items-start">
		<div class="min-w-0">
			<div class="text-text-primary truncate text-xl font-semibold">{node.title}</div>
			<div class="font-mono text-text-faint mt-1 text-xs">{node.drawer.type_line}</div>
		</div>
		<button
			type="button"
			onclick={onclose}
			class="text-text-faint hover:text-text-primary -mt-0.5 -mr-1 ml-auto flex-none cursor-pointer rounded-md p-1 transition-colors"
			aria-label="Close details"
		>
			<X class="size-3.5" />
		</button>
	</div>

	<div class="bg-border-default my-3 h-px"></div>

	<div class="flex flex-col">
		{#each node.drawer.rows as row (row.k)}
			<div class="flex items-baseline gap-3 py-1.75">
				<div class="text-text-faint w-20.5 flex-none text-md">{row.k}</div>
				<div
					class="font-mono text-text-secondary min-w-0 overflow-hidden text-sm text-ellipsis whitespace-nowrap"
				>
					{row.v}
				</div>
			</div>
		{/each}
	</div>

	<div class="flex-1"></div>

	{#if node.service_slug}
		<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
		<a
			href={withEnv(
				resolve('/(app)/projects/[project]/services/[service]', {
					project: projectSlug,
					service: node.service_slug
				}),
				env
			)}
			class="border-accent/40 text-accent-nav hover:bg-accent/10 rounded-[11px] border p-2.5 text-center text-lg font-medium transition-colors"
		>
			Open service →
		</a>
		<!-- eslint-enable svelte/no-navigation-without-resolve -->
	{/if}
</div>
