<script lang="ts">
	let { children } = $props();
</script>

<div class="bg-glow-login flex min-h-screen items-center justify-center px-4 py-12">
	<div class="flex w-[380px] flex-col">
		<div class="mb-6.5 flex flex-col items-center gap-3.5">
			<div
				class="from-accent-from to-accent-to text-surface-base shadow-glow-lg grid h-10.5 w-10.5 place-items-center rounded-xl bg-linear-135 text-[22px] font-bold"
			>
				s
			</div>
			<h1 class="text-text-primary text-[20px] font-semibold tracking-[-0.015em]">
				Sign in to skali
			</h1>
			<div class="font-mono text-text-faint text-[11px]">skali.acme.dev · 3 nodes</div>
		</div>

		{@render children()}

		<div class="font-mono text-text-ghost mt-5.5 text-center text-[10.5px]">
			skali v0.6.1 · self-hosted · <span class="text-text-faint">$ skali login</span>
		</div>
	</div>
</div>
