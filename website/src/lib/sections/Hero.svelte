<script lang="ts">
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import NavBar from '$lib/components/NavBar.svelte';
	import { links } from '$lib/links';
	import StudioOverview from '$lib/mock/StudioOverview.svelte';

	let { version }: { version: string } = $props();
</script>

<section class="relative overflow-hidden">
	<div
		aria-hidden="true"
		class="pointer-events-none absolute inset-x-0 top-0 h-275 bg-blueprint"
	></div>
	<div
		aria-hidden="true"
		class="pointer-events-none absolute top-130 left-1/2 h-200 w-350 -translate-x-1/2 bg-halo"
	></div>

	<NavBar {version} />

	<div
		class="relative mx-auto flex max-w-300 flex-col gap-[clamp(36px,4vw,56px)] px-6 pt-[clamp(96px,11vw,160px)] pb-[clamp(72px,8vw,120px)]"
	>
		<h1
			class="m-0 max-w-250 text-[clamp(44px,8vw,96px)] leading-none font-medium tracking-[-0.055em]"
		>
			A platform for<br />your own servers.
		</h1>
		<div class="flex flex-wrap items-end justify-between gap-8">
			<p class="m-0 max-w-130 text-[clamp(17px,1.8vw,20px)] leading-[1.6] text-text-muted">
				Run your applications on hardware you control,
				<span class="text-text-primary">without running a platform team.</span> Apps, Postgres, S3 buckets,
				TLS, rollouts and backups from one manifest.
			</p>
			<div class="flex flex-wrap gap-3">
				<a
					href="#install"
					class="flex h-12 items-center gap-2.5 rounded-[10px] bg-text-primary px-5.5 text-[15px] font-medium text-surface-base transition-colors hover:bg-white"
				>
					Get started <ArrowRight size={16} />
				</a>
				<a
					href={links.repo}
					rel="external"
					class="flex h-12 items-center rounded-[10px] border border-white/14 px-5.5 text-[15px] text-text-secondary transition-colors hover:border-white/28 hover:text-white"
				>
					View source
				</a>
			</div>
		</div>
	</div>

	<div class="relative mx-auto max-w-336 px-6">
		<div
			role="img"
			aria-label="Skali Studio showing a project overview: four healthy services, request and resource charts, and storage use"
			class="h-165 overflow-hidden rounded-t-[22px] border border-b-0 border-white/10 bg-white/2.5 fade-bottom px-2 pt-2 shadow-[0_-20px_80px_rgb(124_92_255/0.10)]"
		>
			<!-- Phones show the main surface rather than the sidebar. -->
			<div class="max-sm:-ml-[292px]"><StudioOverview /></div>
		</div>
	</div>
</section>
