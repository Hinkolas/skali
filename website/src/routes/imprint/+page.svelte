<script lang="ts">
	import NavBar from '$lib/components/NavBar.svelte';
	import Footer from '$lib/sections/Footer.svelte';

	let { data } = $props();
</script>

<svelte:head>
	<title>Imprint — skali</title>
	<meta name="robots" content="noindex" />
</svelte:head>

<div class="relative">
	<div
		aria-hidden="true"
		class="pointer-events-none absolute inset-x-0 top-0 h-140 bg-blueprint"
	></div>
	<NavBar version={data.release} />
</div>

<!-- Grows so the footer sits at the bottom of a tall window. The rows use
     the home page's section grammar: a mono label column beside the content. -->
<main
	class="relative mx-auto w-full max-w-300 grow px-6 pt-[clamp(72px,9vw,128px)] pb-[clamp(96px,10vw,140px)]"
>
	<div class="flex flex-col gap-6">
		<h1
			class="m-0 text-[clamp(40px,6vw,72px)] leading-none font-medium tracking-[-0.045em] text-balance"
		>
			Imprint <span lang="de" class="text-text-ghost">Impressum</span>
		</h1>
		<p class="m-0 max-w-140 text-[17px] leading-[1.6] text-text-muted">
			skali is a private, non-commercial open-source project. This website is operated by the person
			named below.
		</p>
	</div>

	<section aria-labelledby="mstv" class="mt-[clamp(56px,7vw,96px)]">
		<h2 id="mstv" class="m-0 pb-5 text-[17px] font-medium tracking-[-0.01em]">
			Information pursuant to § 18 (1) MStV
		</h2>
		<address class="not-italic">
			<dl class="m-0 border-t border-border-section">
				<div class="flex flex-wrap gap-x-12 gap-y-2 border-b border-border-section py-5 sm:py-6">
					<dt class="shrink-0 basis-55 pt-1 font-mono text-xs text-text-faint">Name</dt>
					<dd class="m-0 text-lg leading-[1.6] text-text-primary">Nicholas Hinke</dd>
				</div>
				<div class="flex flex-wrap gap-x-12 gap-y-2 border-b border-border-section py-5 sm:py-6">
					<dt class="shrink-0 basis-55 pt-1 font-mono text-xs text-text-faint">Address</dt>
					<dd class="m-0 text-lg leading-[1.6] text-text-secondary">
						c/o KiloHertz GmbH<br />
						Mittagstraße 16d<br />
						39124 Magdeburg<br />
						Germany
					</dd>
				</div>
			</dl>
		</address>
	</section>
</main>
<Footer />
