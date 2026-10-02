<script lang="ts">
	import Check from '@lucide/svelte/icons/check';
	import Copy from '@lucide/svelte/icons/copy';
	import { links } from '$lib/links';

	let { command }: { command: string } = $props();

	let copied = $state(false);
	let timer: ReturnType<typeof setTimeout> | undefined;

	async function copy() {
		try {
			await navigator.clipboard.writeText(command);
		} catch {
			return; // no clipboard access (insecure context, denied): leave the label alone
		}
		copied = true;
		clearTimeout(timer);
		timer = setTimeout(() => (copied = false), 1600);
	}

	// The URL is highlighted inside the displayed command.
	const url = 'https://skali.dev/install.sh';
	const [before, after] = $derived(command.split(url));
</script>

<section id="install" class="scroll-mt-6 px-6 pt-6 pb-35">
	<div
		class="relative mx-auto max-w-300 overflow-hidden rounded-[28px] border border-accent/26 bg-surface-violet p-[clamp(28px,6vw,80px)]"
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
			<div class="flex min-w-0 flex-[1_1_480px] flex-col gap-4.5">
				<!-- The command wraps at its spaces rather than scrolling: while
				     prereleases need SKALI_CHANNEL it is too long for one line. -->
				<div
					class="flex flex-wrap items-center gap-x-4 gap-y-3 rounded-2xl border border-border-strong bg-surface-base/88 py-2.5 pr-2.5 pl-6 shadow-[0_30px_80px_rgb(0_0_0/0.45)]"
				>
					<code
						class="min-w-0 flex-[1_1_16rem] py-2 font-mono text-[clamp(13px,1.4vw,15px)] leading-relaxed break-words text-text-primary"
						><span class="mr-[1ch] text-text-ghost select-none">$</span>{before}<span
							class="text-accent-light">{url}</span
						> <span class="whitespace-nowrap">{after.trim()}</span></code
					>
					<button
						type="button"
						onclick={copy}
						aria-label="Copy install command"
						class="flex h-11.5 min-w-26 shrink-0 cursor-pointer items-center justify-center gap-2 rounded-[10px] bg-text-primary px-4 text-sm font-medium text-surface-base transition-colors hover:bg-white"
					>
						{#if copied}<Check size={15} /> Copied{:else}<Copy size={15} /> Copy{/if}
					</button>
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
				<a href={links.gettingStarted} rel="external" class="text-text-primary hover:text-white"
					>Getting started →</a
				>
			</span>
		</div>
	</div>
</section>
