<script lang="ts">
	import LogoMark from '$lib/components/ui/LogoMark.svelte';
	import { page } from '$app/state';
	import ErrorState from '$lib/components/ui/ErrorState.svelte';
</script>

<svelte:head>
	<title>{page.status} — skali</title>
</svelte:head>

<!-- Boundary for everything outside the app shell (auth pages, layout load
     failures, unmatched top-level URLs). Framed like the login page: logo
     tile with the halo glow centered on it. -->
<div
	class="bg-surface-base relative isolate flex min-h-screen items-center justify-center overflow-hidden px-4 py-12"
>
	<div class="relative flex flex-col items-center">
		<!-- glow centered on the logo tile (23px = half the 46px tile) -->
		<div
			class="bg-glow-halo pointer-events-none absolute top-[23px] left-1/2 -z-10 h-[1150px] w-[1700px] -translate-x-1/2 -translate-y-1/2"
		></div>
		<div
			class="from-accent-from to-accent-to text-surface-base shadow-glow-lg mb-6.5 grid h-10.5 w-10.5 place-items-center rounded-xl bg-linear-135"
		>
			<LogoMark class="h-6 w-6" />
		</div>
		<ErrorState showIcon={false} />
	</div>
</div>
