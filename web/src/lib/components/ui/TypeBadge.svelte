<script lang="ts">
	import type { ServiceKind } from '$lib/mock/types';
	import { SERVICE_KIND_META } from '$lib/service-types';

	// Two forms: 'chip' is the inline icon ×2 pill (project card kind counts);
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
	const Icon = $derived(meta.icon);
</script>

{#if form === 'tile'}
	<span
		class="grid flex-none place-items-center {size === 'md'
			? 'size-7.5 rounded-[10px]'
			: 'size-6.5 rounded-lg'} {meta.text} {meta.bg}"
		title={meta.label}
	>
		<Icon size={size === 'md' ? 16 : 14} />
	</span>
{:else}
	<span
		class="font-mono inline-flex flex-none items-center gap-1 rounded-[6px] px-1.5 py-1 text-2xs font-semibold {meta.text} {meta.bg}"
		title={meta.label}
	>
		<Icon size={13} />{#if count && count > 1}
			<span>×{count}</span>{/if}
	</span>
{/if}
