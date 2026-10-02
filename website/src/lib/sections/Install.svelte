<script lang="ts">
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import Check from '@lucide/svelte/icons/check';
	import Copy from '@lucide/svelte/icons/copy';
	import { links } from '$lib/links';

	const url = 'https://skali.dev/install.sh';
	const command = `curl -fsSL ${url} | sh`;

	let copied = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;
	let code: HTMLElement;

	async function copy() {
		try {
			await navigator.clipboard.writeText(command);
		} catch {
			// No clipboard access (insecure context, denied): select the command
			// so it can be copied by hand, and leave the label alone.
			getSelection()?.selectAllChildren(code);
			return;
		}
		copied = true;
		clearTimeout(timer);
		timer = setTimeout(() => (copied = false), 1600);
	}
</script>

<section id="install" class="scroll-mt-6 px-6 pt-6 pb-35">
	<div
		class="relative mx-auto max-w-330 overflow-hidden rounded-[28px] border border-accent/26 bg-surface-violet p-[clamp(28px,6vw,80px)]"
		style="background-image: radial-gradient(760px 460px at 0% 0%, rgb(124 92 255 / 0.24) 0%, rgb(124 92 255 / 0.06) 55%, transparent 100%)"
	>
		<div
			aria-hidden="true"
			class="pointer-events-none absolute inset-0 bg-[linear-gradient(rgb(255_255_255/0.035)_1px,transparent_1px),linear-gradient(90deg,rgb(255_255_255/0.035)_1px,transparent_1px)] mask-[radial-gradient(ellipse_70%_90%_at_0%_0%,#000_0%,transparent_100%)] bg-size-[56px_56px]"
		></div>

		<div class="relative flex flex-wrap items-center gap-x-18 gap-y-12">
			<div class="flex flex-[1_1_360px] flex-col gap-5">
				<h2
					class="m-0 text-[clamp(34px,4.8vw,56px)] leading-[1.02] font-medium tracking-[-0.05em] text-balance"
				>
					Install skali in one line.
				</h2>
				<p class="m-0 max-w-105 text-[17px] leading-[1.6] text-text-tertiary">
					On your laptop to develop and deploy, and on every server that should become a node.
				</p>
			</div>
			<div class="flex min-w-0 flex-[1.4_1_580px] flex-col gap-4.5">
				<!-- One line from md up; a phone is too narrow for it, so there the
				     command wraps at its spaces instead of scrolling. -->
				<div
					class="flex flex-wrap items-center gap-x-4 gap-y-3 rounded-2xl border border-border-strong bg-surface-base/88 py-2.5 pr-2.5 pl-6 shadow-[0_30px_80px_rgb(0_0_0/0.45)] md:flex-nowrap"
				>
					<code
						bind:this={code}
						class="min-w-0 flex-[1_1_16rem] py-2 font-mono text-[13px] leading-relaxed break-words text-text-primary md:overflow-x-auto md:text-[15px] md:whitespace-nowrap"
						><span class="mr-[1ch] text-text-ghost select-none">$</span>curl -fsSL
						<span class="text-accent-light">{url}</span>
						<span class="whitespace-nowrap">| sh</span></code
					>
					<button
						type="button"
						onclick={copy}
						aria-label="Copy install command"
						class="flex h-11.5 min-w-26 shrink-0 cursor-pointer items-center justify-center gap-2 rounded-[10px] bg-text-primary px-4 text-sm font-medium text-surface-base transition-colors hover:bg-white"
					>
						{#if copied}<Check size={15} /> Copied{:else}<Copy size={15} /> Copy{/if}
					</button>
					<span class="sr-only" aria-live="polite">{copied ? 'Install command copied' : ''}</span>
				</div>
				<div class="flex flex-wrap gap-x-6 gap-y-2 pl-1 font-mono text-xs text-text-muted">
					<span>macOS · Linux</span><span>amd64 · arm64</span><span>checksum verified</span>
				</div>
			</div>
		</div>

		<div
			class="relative mt-[clamp(40px,6vw,64px)] flex flex-wrap justify-between gap-x-8 gap-y-3 border-t border-border-section pt-6 text-sm text-text-tertiary"
		>
			<span>
				Then run <code class="font-mono text-text-primary">sudo skali cluster</code> on a server and follow
				the prompts.
			</span>
			<span class="flex flex-wrap gap-x-7 gap-y-3">
				<span class="text-text-faint">
					Early alpha.
					<a
						href={links.limitations}
						rel="external"
						class="text-text-secondary underline decoration-white/25 underline-offset-3 hover:decoration-white/60"
						>Known limitations</a
					>
				</span>
				<a
					href={links.gettingStarted}
					rel="external"
					class="flex items-center gap-1.5 text-text-primary transition-colors hover:text-white"
				>
					Getting started <ArrowRight size={14} />
				</a>
			</span>
		</div>
	</div>
</section>
