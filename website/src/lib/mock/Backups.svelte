<script lang="ts">
	// A backup schedule and its newest snapshots, as on the Studio's Backups page.
	import CalendarClock from '@lucide/svelte/icons/calendar-clock';

	const snapshots = [
		{ id: '7c1e9a02', age: '12h ago', origin: 'scheduled', size: '1.4 GiB' },
		{ id: 'b4d0e815', age: '1d ago', origin: 'manual', size: '1.4 GiB' },
		{ id: '2a9f61cc', age: '2d ago', origin: 'scheduled', size: '1.3 GiB' }
	];
</script>

<div class="flex w-full flex-col gap-3 leading-normal text-text-primary">
	<div
		class="flex flex-col gap-3 rounded-[15px] border border-border-raised bg-surface-card px-[19.8px] py-4"
	>
		<div class="flex items-center gap-2.5">
			<CalendarClock size={15} class="text-text-ghost" />
			<span class="font-mono text-[13px]">production</span>
			<span
				class="rounded-full bg-white/6 px-[8.8px] py-[2.2px] font-mono text-[10px] text-text-muted"
			>
				complete
			</span>
			<span class="ml-auto font-mono text-[12px] text-text-muted">last 22h ago</span>
		</div>
		<div class="flex flex-col gap-0.5">
			<span class="text-[13px] text-text-secondary">daily at 02:00 UTC · keeps 14 days</span>
			<span class="font-mono text-[12px] text-text-muted">0 2 * * *</span>
		</div>
	</div>
	<div class="overflow-hidden rounded-[15px] border border-border-raised bg-surface-card">
		<div
			class="grid grid-cols-[1fr_1fr_0.8fr] border-b border-border-subtle px-[19.8px] py-[11px] text-[11px] font-semibold tracking-[0.12em] text-text-ghost"
		>
			<span>SNAPSHOT</span><span>ORIGIN</span><span class="text-right">SIZE</span>
		</div>
		{#each snapshots as snapshot (snapshot.id)}
			<div
				class="grid grid-cols-[1fr_1fr_0.8fr] items-center border-b border-border-subtle px-[19.8px] py-3 last:border-b-0"
			>
				<span class="flex flex-col">
					<span class="font-mono text-[13px]">{snapshot.id}</span>
					<span class="text-[11px] text-text-faint">{snapshot.age}</span>
				</span>
				<span
					class={[
						'text-[13px]',
						snapshot.origin === 'scheduled' ? 'text-text-secondary' : 'text-text-muted'
					]}
				>
					{snapshot.origin}
				</span>
				<span class="text-right font-mono text-[12px] text-text-secondary">{snapshot.size}</span>
			</div>
		{/each}
	</div>
</div>
