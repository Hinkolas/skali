<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Command palette',
		archetype: 'palette',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import Search from '@lucide/svelte/icons/search';
	import FolderKanban from '@lucide/svelte/icons/folder-kanban';
	import type { ServiceKind } from '$lib/service-types';
	import type { ServiceView } from '$lib/models/service';
	import type { Project } from '$lib/types/project';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let { close }: { close: () => void } = $props();

	let query = $state('');
	let active = $state(0);
	let input = $state<HTMLInputElement | null>(null);

	$effect(() => {
		input?.focus();
	});

	// Palette data comes from the merged page data: projects everywhere, plus
	// the current project's services when inside a project. Results are
	// deliberately env-free: targets open at the default environment.
	const data = $derived(
		page.data as { projects: Project[]; project?: Project; services?: ServiceView[] }
	);

	type Result = { href: string; title: string; meta: string; kind?: ServiceKind };

	const results = $derived.by(() => {
		const q = query.trim().toLowerCase();
		const all: Result[] = [
			...data.projects.map((p) => ({
				href: resolve('/(app)/projects/[project]', { project: p.name }),
				title: p.display_name || p.name,
				meta: `project · ${p.summary?.environments.length ?? 0} env${
					(p.summary?.environments.length ?? 0) === 1 ? '' : 's'
				}`
			})),
			...(data.project
				? (data.services ?? []).map((s) => ({
						href: resolve('/(app)/projects/[project]/services/[service]', {
							project: data.project!.name,
							service: s.key
						}),
						title: s.name,
						meta: data.project!.name,
						kind: s.type as ServiceKind
					}))
				: [])
		];
		return q ? all.filter((r) => r.title.toLowerCase().includes(q)) : all;
	});

	function go(href: string) {
		close();
		// eslint-disable-next-line svelte/no-navigation-without-resolve -- result hrefs are built with resolve() above
		void goto(href);
	}

	function onKey(e: KeyboardEvent) {
		if (e.key === 'ArrowDown') {
			e.preventDefault();
			active = Math.min(active + 1, results.length - 1);
		} else if (e.key === 'ArrowUp') {
			e.preventDefault();
			active = Math.max(active - 1, 0);
		} else if (e.key === 'Enter' && results[active]) {
			e.preventDefault();
			go(results[active].href);
		}
	}
</script>

<div class="border-border-subtle flex items-center gap-2.5 border-b px-4 py-3">
	<Search size={17} class="text-text-ghost flex-none" />
	<input
		bind:this={input}
		bind:value={query}
		type="search"
		name="palette-search"
		autocomplete="off"
		spellcheck="false"
		data-1p-ignore
		data-lpignore="true"
		data-bwignore
		placeholder="Jump to a project or service…"
		class="text-text-primary w-full bg-transparent text-lg focus:outline-none"
		oninput={() => (active = 0)}
		onkeydown={onKey}
	/>
	<kbd
		class="font-mono border-border-strong text-text-ghost flex-none rounded-[6px] border px-1.25 py-px text-xs"
	>
		esc
	</kbd>
</div>

<div class="min-h-0 flex-1 overflow-y-auto p-2">
	{#each results as result, i (result.href)}
		<button
			type="button"
			onclick={() => go(result.href)}
			onpointerenter={() => (active = i)}
			class="flex w-full cursor-pointer items-center gap-2.5 rounded-[11px] px-3 py-2 text-left transition-colors {i ===
			active
				? 'bg-white/6'
				: 'hover:bg-white/4'}"
		>
			{#if result.kind}
				<TypeBadge kind={result.kind} form="tile" />
			{:else}
				<FolderKanban size={15} class="text-text-tertiary flex-none" />
			{/if}
			<span class="text-text-secondary truncate text-base font-medium">{result.title}</span>
			<span class="font-mono text-text-faint ml-auto flex-none text-xs">{result.meta}</span>
		</button>
	{:else}
		<div class="text-text-ghost px-3 py-6 text-center text-base">No matches</div>
	{/each}
</div>

<div
	class="border-border-subtle bg-surface-raised/50 text-text-ghost flex items-center gap-3 border-t px-4 py-2 text-xs"
>
	<span class="flex items-center gap-1">
		<kbd class="font-mono border-border-strong rounded-[5px] border px-1 py-px">↑↓</kbd> navigate
	</span>
	<span class="flex items-center gap-1">
		<kbd class="font-mono border-border-strong rounded-[5px] border px-1 py-px">↵</kbd> open
	</span>
</div>
