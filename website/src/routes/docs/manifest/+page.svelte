<script lang="ts">
	import NavBar from '$lib/components/NavBar.svelte';
	import { links } from '$lib/links';
	import Footer from '$lib/sections/Footer.svelte';

	let { data } = $props();

	const title = 'The skali.yaml manifest — skali';
	const description =
		'Every skali.yaml field, default, unit and validation rule, for the newest skali release.';
</script>

<svelte:head>
	<title>{title}</title>
	<meta name="description" content={description} />
	<link rel="canonical" href="https://skali.dev/docs/manifest" />
	<meta property="og:type" content="article" />
	<meta property="og:url" content="https://skali.dev/docs/manifest" />
	<meta property="og:title" content={title} />
	<meta property="og:description" content={description} />
</svelte:head>

<div class="relative">
	<div
		aria-hidden="true"
		class="pointer-events-none absolute inset-x-0 top-0 h-140 bg-blueprint"
	></div>
	<NavBar version={data.release} />
</div>

<main
	class="relative mx-auto w-full max-w-300 grow px-6 pt-[clamp(72px,9vw,128px)] pb-[clamp(96px,10vw,140px)]"
>
	<!-- The release sits above the title: the reference's opening line says
	     the release it describes is named above it. -->
	<header class="flex flex-col gap-6">
		<p class="m-0 flex gap-3 font-mono text-xs">
			<span class="text-accent-light">Reference</span>
			<span class="text-text-faint">skali {data.ref}</span>
		</p>
		<h1
			class="m-0 text-[clamp(40px,6vw,72px)] leading-none font-medium tracking-[-0.045em] text-balance"
		>
			{data.title}
		</h1>
	</header>

	<section aria-label="Release" class="mt-[clamp(40px,5vw,64px)]">
		<dl class="m-0 border-t border-border-section">
			<div class="flex flex-wrap gap-x-12 gap-y-2 border-b border-border-section py-5">
				<dt class="shrink-0 basis-55 pt-0.5 font-mono text-xs text-text-faint">This page</dt>
				<dd class="m-0 max-w-160 text-[15px] leading-[1.6] text-text-tertiary">
					Describes <span class="font-mono text-text-primary">{data.ref}</span>, the newest release,
					and follows each new one.
				</dd>
			</div>
			<div class="flex flex-wrap gap-x-12 gap-y-2 border-b border-border-section py-5">
				<dt class="shrink-0 basis-55 pt-0.5 font-mono text-xs text-text-faint">Your release</dt>
				<dd class="m-0 max-w-160 text-[15px] leading-[1.6] text-text-tertiary">
					Run <code class="font-mono text-text-primary">skali skill read manifest</code> in your
					project directory. It prints this reference for the release your project's cluster runs,
					the one that compiles your manifest (<a
						href={links.versioning}
						rel="external"
						class="text-text-secondary underline decoration-border-strong underline-offset-3 transition-colors hover:text-text-primary"
						>how the CLI matches releases</a
					>).
				</dd>
			</div>
			<div class="flex flex-wrap gap-x-12 gap-y-2 border-b border-border-section py-5">
				<dt class="shrink-0 basis-55 pt-0.5 font-mono text-xs text-text-faint">Source</dt>
				<dd class="m-0 max-w-160 text-[15px] leading-[1.6] text-text-tertiary">
					<a
						href={links.file(data.ref, data.path)}
						rel="external"
						class="font-mono text-[13px] break-all text-text-secondary underline decoration-border-strong underline-offset-3 transition-colors hover:text-text-primary"
						>{data.path}</a
					>
					at <span class="font-mono text-text-primary">{data.ref}</span>. Switch the tag on GitHub
					to read another release.
				</dd>
			</div>
		</dl>
	</section>

	<div class="mt-[clamp(56px,7vw,96px)] flex gap-12">
		<nav aria-label="On this page" class="hidden w-55 shrink-0 xl:block">
			<div class="sticky top-8 flex flex-col gap-3">
				<span class="font-mono text-[11px] tracking-[0.08em] text-text-faint uppercase">
					On this page
				</span>
				<ul class="m-0 flex list-none flex-col gap-2 p-0">
					{#each data.headings as heading (heading.id)}
						<li class={heading.depth === 3 ? 'pl-3' : ''}>
							<a
								href="#{heading.id}"
								class="block text-text-muted transition-colors hover:text-text-primary {heading.depth ===
								3
									? 'text-[13px]'
									: 'text-sm'}">{heading.text}</a
							>
						</li>
					{/each}
				</ul>
			</div>
		</nav>

		<!-- eslint-disable-next-line svelte/no-at-html-tags -- rendered at build time from the repository's own reference -->
		<article class="reference max-w-190 min-w-0 grow">{@html data.html}</article>
	</div>
</main>
<Footer />

<style>
	.reference {
		font-size: 16px;
		line-height: 1.7;
		color: var(--color-text-muted);
	}
	.reference :global(h2) {
		margin: 80px 0 16px;
		font-size: clamp(28px, 3vw, 36px);
		line-height: 1.1;
		font-weight: 500;
		letter-spacing: -0.03em;
		color: var(--color-text-primary);
		scroll-margin-top: 32px;
	}
	.reference :global(h2:first-child) {
		margin-top: 0;
	}
	.reference :global(h3) {
		margin: 56px 0 12px;
		font-size: 22px;
		line-height: 1.2;
		font-weight: 500;
		letter-spacing: -0.02em;
		color: var(--color-text-primary);
		scroll-margin-top: 32px;
	}
	.reference :global(p),
	.reference :global(ul) {
		margin: 16px 0;
	}
	.reference :global(ul) {
		padding-left: 20px;
		list-style: disc;
	}
	.reference :global(li + li) {
		margin-top: 8px;
	}
	.reference :global(li::marker) {
		color: var(--color-text-ghost);
	}
	/* Emphasis steps up the text ramp rather than the weight. */
	.reference :global(strong) {
		font-weight: inherit;
		color: var(--color-text-primary);
	}
	.reference :global(a) {
		color: var(--color-text-secondary);
		text-decoration: underline;
		text-decoration-color: var(--color-border-strong);
		text-underline-offset: 3px;
	}
	.reference :global(:not(pre) > code) {
		font-family: var(--font-mono);
		font-size: 0.875em;
		color: var(--color-text-secondary);
		background: var(--color-surface-input);
		border: 1px solid var(--color-border-default);
		border-radius: 6px;
		padding: 1px 5px;
		overflow-wrap: anywhere;
	}
	.reference :global(pre) {
		position: relative;
		margin: 24px 0;
		padding: 20px 24px;
		overflow-x: auto;
		background: var(--color-surface-input);
		border: 1px solid var(--color-border-default);
		border-radius: 16px;
		font-family: var(--font-mono);
		font-size: 14px;
		line-height: 1.65;
		color: var(--color-text-tertiary);
	}
	/* Complete manifests, as opposed to excerpts, carry their file name. */
	.reference :global(pre[data-file]) {
		padding-top: 44px;
	}
	.reference :global(pre[data-file]::before) {
		content: attr(data-file);
		position: absolute;
		top: 16px;
		left: 24px;
		font-size: 12px;
		color: var(--color-text-faint);
	}
	.reference :global(.yaml-key) {
		color: var(--color-text-primary);
	}
	.reference :global(.yaml-comment) {
		color: var(--color-text-faint);
	}
	.reference :global(table) {
		display: block;
		overflow-x: auto;
		margin: 24px 0;
		border-collapse: collapse;
		font-size: 15px;
	}
	.reference :global(th) {
		padding: 0 24px 12px 0;
		text-align: left;
		font-family: var(--font-mono);
		font-size: 12px;
		font-weight: 400;
		color: var(--color-text-faint);
		border-bottom: 1px solid var(--color-border-section);
	}
	.reference :global(td) {
		padding: 12px 24px 12px 0;
		color: var(--color-text-tertiary);
		border-bottom: 1px solid var(--color-border-section);
	}
</style>
