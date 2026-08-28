<script lang="ts">
	import { page } from '$app/state';
	import type { LayoutData } from './$types';

	let { data, children }: { data: LayoutData; children: import('svelte').Snippet } = $props();

	// Each auth page names itself through its load data; sign-in is the default.
	const title = $derived((page.data.title as string | undefined) ?? 'Sign in to skali');
</script>

<div
	class="bg-surface-base relative isolate flex min-h-screen items-center justify-center overflow-hidden px-4 py-12"
>
	<div class="flex w-[418px] flex-col">
		<div class="relative mb-6.5 flex flex-col items-center gap-3.5">
			<!-- glow centered on the logo tile (23px = half the 46px tile) -->
			<div
				class="bg-glow-halo pointer-events-none absolute top-[23px] left-1/2 -z-10 h-[1150px] w-[1700px] -translate-x-1/2 -translate-y-1/2"
			></div>
			<div
				class="from-accent-from to-accent-to text-surface-base shadow-glow-lg grid h-10.5 w-10.5 place-items-center rounded-xl bg-linear-135 text-3xl font-bold"
			>
				s
			</div>
			<h1 class="text-text-primary text-2xl font-semibold tracking-[-0.015em]">{title}</h1>
		</div>

		{@render children()}

		{#if data.version}
			<div class="font-mono text-text-ghost mt-5.5 text-center text-xs">skali {data.version}</div>
		{/if}
	</div>
</div>
