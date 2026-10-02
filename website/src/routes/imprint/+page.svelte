<script lang="ts">
	import NavBar from '$lib/components/NavBar.svelte';
	import Footer from '$lib/sections/Footer.svelte';

	let { data } = $props();
</script>

<svelte:head>
	<title>Imprint — skali</title>
	<meta name="robots" content="noindex" />
</svelte:head>

<NavBar version={data.release} />
<main class="mx-auto max-w-200 px-6 pt-16 pb-24">
	<h1 class="m-0 text-[40px] leading-[1.1] font-medium tracking-[-0.03em]">Imprint</h1>
	<p class="mt-3 mb-0 font-mono text-[11px] tracking-[0.08em] text-text-faint uppercase">
		Impressum
	</p>

	<section class="mt-12 flex flex-col gap-3">
		<h2 class="m-0 text-base font-semibold">Information pursuant to § 18 (1) MStV</h2>
		<address class="text-[15px] leading-[1.7] text-text-secondary not-italic">
			Nicholas Hinke<br />
			c/o KiloHertz GmbH<br />
			Mittagstraße 16d<br />
			39124 Magdeburg<br />
			Germany
		</address>
	</section>

	<p class="mt-10 mb-0 max-w-140 text-[15px] leading-[1.7] text-text-muted">
		skali is a private, non-commercial open-source project. This website is operated by the person
		named above.
	</p>
</main>
<Footer />
