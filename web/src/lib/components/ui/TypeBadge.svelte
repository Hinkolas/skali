<script lang="ts">
	import type { ServiceKind } from '$lib/mock/types';
	import { SERVICE_KIND_META } from '$lib/service-types';

	// Two forms: 'chip' is the inline AP ×2 pill (project cards, sidebar rows);
	// 'tile' is the square icon block next to service names (sm 26px, md 30px).
	let {
		kind,
		form = 'chip',
		size = 'sm',
		count
	}: {
		kind: ServiceKind;
		form?: 'chip' | 'tile';
		size?: 'sm' | 'md';
		count?: number;
	} = $props();

	const meta = $derived(SERVICE_KIND_META[kind]);
</script>

{#if form === 'tile'}
	<span
		class="font-mono grid flex-none place-items-center font-semibold {size === 'md'
			? 'size-7.5 rounded-[9px] text-[9.5px]'
			: 'size-6.5 rounded-lg text-[9px]'} {meta.text} {meta.bg}"
	>
		{meta.code}
	</span>
{:else}
	<span
		class="font-mono flex-none rounded-[5px] px-1.5 py-0.5 text-[9px] font-semibold {meta.text} {meta.bg}"
	>
		{meta.code}{#if count && count > 1}
			×{count}{/if}
	</span>
{/if}
