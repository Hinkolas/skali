<script lang="ts">
	// The Studio's promote finder, opened from staging.
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import Lock from '@lucide/svelte/icons/lock';
	import Search from '@lucide/svelte/icons/search';
</script>

<div
	class="w-full overflow-hidden rounded-2xl border border-border-strong bg-surface-card leading-normal text-text-primary shadow-[0_24px_60px_rgb(0_0_0/0.45)]"
>
	<div class="flex items-center gap-3 border-b border-border-subtle px-[17.6px] py-[13.2px]">
		<Search size={17} class="text-text-ghost" />
		<span class="grow text-[15px] text-text-ghost">Promote staging to…</span>
		<span
			class="rounded-md border border-border-strong px-1.5 py-px font-mono text-[11px] text-text-faint"
		>
			esc
		</span>
	</div>
	<div class="flex flex-col gap-0.5 p-[6.6px]">
		<div class="flex items-center gap-2.5 rounded-[11px] bg-white/6 px-[13.2px] py-[9.9px]">
			<span class="font-mono text-[13px]">production</span>
			<span
				class="rounded-full bg-status-warning/10 px-[8.8px] py-[2.2px] font-mono text-[10px] text-status-warning"
			>
				protected
			</span>
			<ArrowRight size={13} class="ml-auto text-text-tertiary" />
		</div>
		<div class="flex items-center gap-2.5 rounded-[11px] px-[13.2px] py-[9.9px]">
			<span class="font-mono text-[13px]">preview</span>
			<span
				class="rounded-full bg-white/6 px-[8.8px] py-[2.2px] font-mono text-[10px] text-text-muted"
			>
				last used
			</span>
		</div>
		<div class="flex flex-col gap-0.5 rounded-[11px] px-[13.2px] py-[9.9px]">
			<span class="flex items-center gap-2 font-mono text-[13px] text-text-ghost">
				<Lock size={12} /> demo
			</span>
			<span class="text-[12px] text-text-faint">locked for you</span>
		</div>
	</div>
	<div
		class="flex gap-4 border-t border-border-subtle px-[17.6px] py-2.5 text-[11px] text-text-ghost"
	>
		<span>↑↓ navigate</span><span>↵ select</span>
	</div>
</div>
