<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Command palette',
		panelClass: 'mt-[12vh] flex max-h-[60vh] w-full flex-col self-start sm:max-w-xl'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import Search from '@lucide/svelte/icons/search';
	import FolderKanban from '@lucide/svelte/icons/folder-kanban';
	import type { ServiceKind } from '$lib/mock/types';
	import { PROJECTS, SERVICES } from '$lib/mock/data';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let { close }: { close: () => void } = $props();

	let query = $state('');
	let input = $state<HTMLInputElement | null>(null);

	$effect(() => {
		input?.focus();
	});

	// Palette results are deliberately env-free: targets open at the
	// project's default environment.
	type Result = { href: string; title: string; meta: string; kind?: ServiceKind };

	const results = $derived.by(() => {
		const q = query.trim().toLowerCase();
		const all: Result[] = [
			...PROJECTS.map((p) => ({
				href: resolve('/(app)/projects/[project]', { project: p.slug }),
				title: p.name,
				meta: `project · ${p.environments.length} env${p.environments.length === 1 ? '' : 's'}`
			})),
			...SERVICES.map((s) => ({
				href: resolve('/(app)/projects/[project]/services/[service]', {
					project: s.project_slug,
					service: s.slug
				}),
				title: s.name,
				meta: s.project_slug,
				kind: s.type
			}))
		];
		return q ? all.filter((r) => r.title.toLowerCase().includes(q)) : all;
	});

	function go(href: string) {
		close();
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- result hrefs are built with resolve() above
		void goto(href);
	}
</script>

<div class="border-border-subtle flex items-center gap-2.5 border-b px-4 py-3">
	<Search size={17} class="text-text-ghost flex-none" />
	<input
		bind:this={input}
		bind:value={query}
		type="text"
		placeholder="Jump to a project or service…"
		class="text-text-primary w-full bg-transparent text-lg focus:outline-none"
		onkeydown={(e) => {
			if (e.key === 'Enter' && results.length > 0) {
				e.preventDefault();
				go(results[0].href);
			}
		}}
	/>
	<kbd
		class="font-mono border-border-strong text-text-ghost flex-none rounded-[6px] border px-1.25 py-px text-xs"
	>
		esc
	</kbd>
</div>

<div class="min-h-0 flex-1 overflow-y-auto p-2">
	{#each results as result (result.href)}
		<button
			type="button"
			onclick={() => go(result.href)}
			class="flex w-full cursor-pointer items-center gap-2.5 rounded-[11px] px-3 py-2 text-left transition-colors hover:bg-white/4"
		>
			{#if result.kind}
				<TypeBadge kind={result.kind} form="tile" />
			{:else}
				<FolderKanban size={15} class="text-text-tertiary flex-none" />
			{/if}
			<span class="text-text-secondary truncate text-base font-medium">{result.title}</span>
			<span class="font-mono text-text-ghost ml-auto flex-none text-xs">{result.meta}</span>
		</button>
	{:else}
		<div class="text-text-ghost px-3 py-6 text-center text-base">No matches</div>
	{/each}
</div>
