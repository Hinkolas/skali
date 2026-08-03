<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { NavIcon } from '$lib/navigation';

	let {
		icon: Icon,
		title,
		description,
		class: className = '',
		action
	}: {
		icon?: NavIcon;
		title: string;
		description?: string;
		class?: string;
		action?: Snippet;
	} = $props();
</script>

<div
	class="border-border-strong grid min-h-[242px] place-items-center rounded-[15px] border border-dashed {className}"
>
	<div class="flex flex-col items-center gap-2.5 p-8 text-center">
		{#if Icon}
			<div class="text-text-ghost bg-white/4 mb-1 grid size-10 place-items-center rounded-xl">
				<Icon size={20} strokeWidth={1.75} />
			</div>
		{/if}
		<div class="text-text-muted text-lg font-medium">{title}</div>
		{#if description}
			<div class="font-mono text-text-ghost text-xs">{description}</div>
		{/if}
		{#if action}
			<div class="mt-2">{@render action()}</div>
		{/if}
	</div>
</div>
