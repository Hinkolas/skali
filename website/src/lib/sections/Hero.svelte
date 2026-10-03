<script lang="ts">
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import NavBar from '$lib/components/NavBar.svelte';
	import { links } from '$lib/links';
	import StudioOverview from '$lib/mock/StudioOverview.svelte';

	let { version }: { version: string } = $props();

	// The headline, line by line, so each word can arrive on its own.
	const headline = [
		['A', 'platform', 'for'],
		['your', 'own', 'servers.']
	];
</script>

<section class="relative overflow-hidden">
	<div
		aria-hidden="true"
		class="intro-grid pointer-events-none absolute inset-x-0 top-0 h-275 bg-blueprint"
	></div>
	<div
		aria-hidden="true"
		class="intro-halo pointer-events-none absolute top-130 left-1/2 h-200 w-350 -translate-x-1/2 bg-halo"
	></div>

	<div class="intro-nav relative z-10"><NavBar {version} /></div>

	<div
		class="relative mx-auto flex max-w-300 flex-col gap-[clamp(36px,4vw,56px)] px-6 pt-[clamp(96px,11vw,160px)] pb-[clamp(72px,8vw,120px)]"
	>
		<h1
			class="m-0 max-w-250 text-[clamp(44px,8vw,96px)] leading-none font-medium tracking-[-0.055em]"
		>
			{#each headline as line, l (l)}
				{#each line as word, w (w)}
					<!-- The space is the words' own; a block would trim it. -->
					<!-- eslint-disable-next-line svelte/no-useless-mustaches -->
					<span class="intro-word" style:--n={l * 3 + w}>{word}</span>{' '}
				{/each}
				{#if l === 0}<br />{/if}
			{/each}
		</h1>
		<div class="flex flex-wrap items-end justify-between gap-8">
			<p
				class="intro-rise m-0 max-w-130 text-[clamp(17px,1.8vw,20px)] leading-[1.6] text-text-muted"
			>
				Run your applications on hardware you control,
				<span class="text-text-primary">without running a platform team.</span> Apps, Postgres, S3 buckets,
				TLS, rollouts and backups from one manifest.
			</p>
			<div class="intro-rise flex flex-wrap gap-3" style:--delay="820ms">
				<a
					href="#install"
					class="group flex h-12 items-center gap-2.5 rounded-[10px] bg-text-primary px-5.5 text-[15px] font-medium text-surface-base transition-colors hover:bg-white"
				>
					Get started
					<ArrowRight
						size={16}
						class="transition-transform duration-200 ease-out group-hover:translate-x-0.5"
					/>
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

	<div class="intro-frame relative mx-auto max-w-336 px-6">
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

<style>
	/* The opening: the grid and the light come up, the headline arrives word
	   by word out of a blur, the lead and actions follow, then Studio rises
	   into the light and its charts draw (see StudioOverview). Plain CSS on
	   load, so it starts with the first paint and always ends whole. */
	@media (prefers-reduced-motion: no-preference) {
		.intro-grid {
			animation: intro-fade 1.6s ease-out both;
		}
		.intro-halo {
			animation: intro-light 2.2s cubic-bezier(0.16, 1, 0.3, 1) 0.5s both;
		}
		.intro-nav {
			animation: intro-fade 0.8s ease-out 0.1s both;
		}
		.intro-word {
			display: inline-block;
			animation: intro-word 1.1s cubic-bezier(0.16, 1, 0.3, 1) both;
			animation-delay: calc(150ms + var(--n) * 70ms);
		}
		.intro-rise {
			animation: intro-rise 1s cubic-bezier(0.16, 1, 0.3, 1) both;
			animation-delay: var(--delay, 700ms);
		}
		.intro-frame {
			animation: intro-frame 1.4s cubic-bezier(0.16, 1, 0.3, 1) 0.85s both;
		}
	}

	@keyframes intro-fade {
		from {
			opacity: 0;
		}
	}

	@keyframes intro-light {
		from {
			opacity: 0;
			scale: 0.7;
		}
	}

	@keyframes intro-word {
		from {
			opacity: 0;
			transform: translateY(0.35em);
			filter: blur(12px);
		}
	}

	@keyframes intro-rise {
		from {
			opacity: 0;
			transform: translateY(16px);
		}
	}

	@keyframes intro-frame {
		from {
			opacity: 0;
			transform: translateY(72px);
		}
	}
</style>
