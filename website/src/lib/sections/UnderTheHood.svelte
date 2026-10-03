<script lang="ts">
	import { onMount } from 'svelte';

	// The statement, with the parts skali is built on between asterisks.
	const statement =
		"skali runs on *k3s*, routes through *Traefik* with certificates from *Let's Encrypt*, keeps databases on *CloudNativePG* and objects on *SeaweedFS*. Proven parts, installed and operated for you. You never have to touch Kubernetes.";

	// Words, each split into its plain and named parts ("*k3s*," is a named
	// "k3s" and a plain ","). Every word keeps its trailing space, since the
	// block below would trim one written between the words.
	const words: { text: string; named: boolean }[][] = [[]];
	statement.split('*').forEach((segment, s) => {
		for (const piece of segment.split(/(?<= )/)) {
			words[words.length - 1].push({ text: piece, named: s % 2 === 1 });
			if (piece.endsWith(' ')) words.push([]);
		}
	});

	let paragraph: HTMLParagraphElement;
	let reading = $state(false);

	// As the statement scrolls up the screen it is read out: words light up
	// in order, the plain ones to the statement's gray and the named parts to
	// full white, with a soft edge a few words wide. Read through, it is the
	// statement as it stands without scripts or with reduced motion.
	onMount(() => {
		if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
		reading = true;

		const edge = 4; // words between unlit and fully lit
		let frame = 0;
		const measure = () => {
			frame = 0;
			const box = paragraph.getBoundingClientRect();
			// From the statement's middle at 85% of the screen to 40%.
			const middle = box.top + box.height / 2;
			const progress = Math.min(
				1,
				Math.max(0, (0.85 * innerHeight - middle) / (0.45 * innerHeight))
			);
			paragraph.style.setProperty('--read', (progress * (words.length + edge)).toFixed(3));
		};
		const schedule = () => {
			frame ||= requestAnimationFrame(measure);
		};

		measure();
		addEventListener('scroll', schedule, { passive: true });
		addEventListener('resize', schedule);
		return () => {
			removeEventListener('scroll', schedule);
			removeEventListener('resize', schedule);
			cancelAnimationFrame(frame);
		};
	});
</script>

<section class="mx-auto flex max-w-300 flex-wrap gap-x-12 gap-y-6 px-6 py-35">
	<div class="flex shrink-0 basis-55 gap-3 pt-3.5 font-mono text-xs">
		<span class="text-accent-light">03</span>
		<span class="text-text-faint">Under the hood</span>
	</div>
	<p
		bind:this={paragraph}
		class:reading
		class="m-0 max-w-220 flex-[1_1_600px] text-[clamp(26px,3.4vw,40px)] leading-[1.3] font-normal tracking-[-0.03em] text-text-ghost"
	>
		{#each words as parts, i (i)}
			<span class="word" style:--i={i}
				>{#each parts as part, j (j)}{#if part.named}<span class="named text-text-primary"
							>{part.text}</span
						>{:else}{part.text}{/if}{/each}</span
			>
		{/each}
	</p>
</section>

<style>
	/* How lit a word is, 0 to 1, from how far the reading has come. */
	.reading .word {
		--lit: clamp(0, calc((var(--read, 0) - var(--i)) / 4), 1);
		color: color-mix(
			in srgb,
			var(--color-text-ghost) calc(var(--lit) * 100%),
			rgb(255 255 255 / 0.13)
		);
	}
	.reading .named {
		color: color-mix(
			in srgb,
			var(--color-text-primary) calc(var(--lit) * 100%),
			rgb(255 255 255 / 0.13)
		);
	}
</style>
