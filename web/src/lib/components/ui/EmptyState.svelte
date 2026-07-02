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
	class="border-border-strong grid min-h-[220px] place-items-center rounded-[14px] border border-dashed {className}"
>
	<div class="flex flex-col items-center gap-2.5 p-8 text-center">
		{#if Icon}
			<div class="text-text-ghost bg-white/4 mb-1 grid size-10 place-items-center rounded-xl">
				<Icon size={18} strokeWidth={1.75} />
			</div>
		{/if}
		<div class="text-text-muted text-[13.5px] font-medium">{title}</div>
		{#if description}
			<div class="font-mono text-text-ghost text-[10.5px]">{description}</div>
		{/if}
		{#if action}
			<div class="mt-2">{@render action()}</div>
		{/if}
	</div>
</div>
