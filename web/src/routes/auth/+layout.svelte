<script lang="ts">
	let { children } = $props();
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
			<h1 class="text-text-primary text-2xl font-semibold tracking-[-0.015em]">Sign in to skali</h1>
			<div class="font-mono text-text-faint text-sm">skali.acme.dev · 3 nodes</div>
		</div>

		{@render children()}

		<div class="font-mono text-text-ghost mt-5.5 text-center text-xs">
			skali v0.6.1 · self-hosted · <span class="text-text-faint">$ skali login</span>
		</div>
	</div>
</div>
