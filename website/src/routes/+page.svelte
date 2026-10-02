<script lang="ts">
	import Footer from '$lib/sections/Footer.svelte';
	import Hero from '$lib/sections/Hero.svelte';
	import Install from '$lib/sections/Install.svelte';
	import Platform from '$lib/sections/Platform.svelte';
	import UnderTheHood from '$lib/sections/UnderTheHood.svelte';
	import Workflow from '$lib/sections/Workflow.svelte';

	let { data } = $props();

	const title = 'skali — a platform for your own servers';
	const description =
		'Run your applications on hardware you control, without running a platform team. Apps, Postgres, S3 buckets, TLS, rollouts and backups from one manifest.';
</script>

<svelte:head>
	<title>{title}</title>
	<meta name="description" content={description} />
	<link rel="canonical" href="https://skali.dev/" />
	<meta property="og:type" content="website" />
	<meta property="og:url" content="https://skali.dev/" />
	<meta property="og:title" content={title} />
	<meta property="og:description" content={description} />
</svelte:head>

<main>
	<Hero version={data.release} />
	<Workflow />
	<Platform />
	<UnderTheHood />
	<Install />
</main>
<Footer />
