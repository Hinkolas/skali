<script lang="ts">
	import Server from '@lucide/svelte/icons/server';

	let { children } = $props();
</script>

<div
	class="relative flex min-h-screen items-center justify-center overflow-hidden bg-surface-base px-4 py-12"
>
	<div
		class="pointer-events-none absolute inset-0 z-0 opacity-[0.07]"
		style="background-image: radial-gradient(#71717a 1px, transparent 1px); background-size: 24px 24px;"
	></div>

	<div class="relative z-10 w-full max-w-md">
		<div class="mb-6 flex flex-col items-center gap-2.5">
			<div
				class="flex h-12 w-12 items-center justify-center rounded-2xl bg-surface-overlay text-accent shadow-sm ring-1 ring-border-default"
			>
				<Server class="h-6 w-6" />
			</div>
			<span class="text-sm font-medium text-text-secondary">skali</span>
		</div>

		{@render children()}
	</div>
</div>
