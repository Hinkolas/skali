<script lang="ts">
	import { onMount } from 'svelte';
	import SectionHeader from '$lib/components/SectionHeader.svelte';
	import { reveal } from '$lib/motion';

	const steps = [
		{
			label: 'Develop',
			command: 'skali dev',
			text: 'Your app hot-reloads on your laptop while its databases and buckets run in a disposable local cluster.'
		},
		{
			label: 'Deploy',
			command: 'skali deploy',
			text: 'Builds the image, runs release commands for migrations, and rolls out blue-green behind health checks.'
		},
		{
			label: 'Promote',
			command: 'skali deploy --from staging',
			text: 'Move the revision you tested on staging to production. Values stay encrypted per environment.'
		},
		{
			label: 'Recover',
			command: 'skali rollback',
			text: 'When a revision misbehaves, go back to the one before it. No rebuild, no guesswork.'
		}
	];

	let list: HTMLOListElement;
	let tracing = $state(false);

	// Scroll progress of the trace, per step: `--p` is how far along its
	// stretch of the rule the trace is, `--d` how lit its dot is. Above one
	// column, the steps take turns in one stretch of the list's passage up the
	// screen, so the trace reads as a single line across a row; in one column,
	// each step follows its own passage.
	onMount(() => {
		if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
		tracing = true;

		const items = [...list.children] as HTMLElement[];
		const single = matchMedia('(width < 48rem)');
		const clamp = (x: number) => Math.min(1, Math.max(0, x));
		// 0 when the element's top enters at the bottom of the screen, 1 when
		// its bottom leaves at the top.
		const passage = (el: Element) => {
			const box = el.getBoundingClientRect();
			return (innerHeight - box.top) / (innerHeight + box.height);
		};

		let frame = 0;
		const measure = () => {
			frame = 0;
			const whole = passage(list);
			items.forEach((item, i) => {
				const [at, from, to] = single.matches
					? [passage(item), 0.22, 0.46]
					: [whole, 0.12 + i * 0.0825, 0.2025 + i * 0.0825];
				item.style.setProperty('--p', clamp((at - from) / (to - from)).toFixed(4));
				item.style.setProperty('--d', clamp((at - from) / ((to - from) * 0.15)).toFixed(4));
			});
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

<section id="workflow" class="mx-auto flex max-w-330 scroll-mt-6 flex-col gap-16 px-6 py-35">
	<!-- As in Platform: the timeline runs wider so each command fits on one
	     line, while the header keeps the measure of the other sections. -->
	<div use:reveal class="mx-auto w-full max-w-288">
		<SectionHeader
			index="01"
			label="Workflow"
			title="The same manifest, on your laptop and on your servers."
		>
			<p class="m-0 max-w-140 text-[17px] leading-[1.6] text-text-muted">
				Develop against the real platform, not an approximation of it. What works locally is what
				ships.
			</p>
		</SectionHeader>
	</div>
	<!-- Two columns until four fit with each command on one line, so no row
	     is left with a single step. Every step draws its own stretch of the
	     rule, which keeps a wrapped row on a line of its own. -->
	<!-- As the list scrolls up the screen, a trace runs along the rule and
	     lights each step's dot as it arrives. Without scripts, and with reduced
	     motion, the dots are simply lit. -->
	<ol
		bind:this={list}
		class:tracing
		class="m-0 grid list-none gap-y-10 p-0 md:grid-cols-2 xl:grid-cols-4"
	>
		{#each steps as step, i (step.label)}
			<li
				use:reveal={{ delay: 90 * i }}
				class="relative flex flex-col gap-3.5 border-t border-white/10 pt-8 pr-7"
				style:--i={i}
			>
				<span aria-hidden="true" class="trace"
					><span class="fill"></span><span class="head"></span></span
				>
				<span
					aria-hidden="true"
					class="dot absolute -top-1 left-0 size-[7px] rounded-full bg-accent-light"
				></span>
				<span class="font-mono text-xs text-text-faint">{step.label}</span>
				<code class="font-mono text-[17px] text-text-primary">{step.command}</code>
				<p class="m-0 text-[15px] leading-[1.6] text-text-muted">{step.text}</p>
			</li>
		{/each}
	</ol>
</section>

<style>
	.trace {
		display: none;
	}

	/* A 25px band centred on the 1px rule, clipped so the head never reaches
	   past its own step. */
	.tracing .trace {
		position: absolute;
		inset: -13px 0 auto;
		display: block;
		height: 25px;
		overflow: clip;
		pointer-events: none;
	}

	.fill,
	.head {
		position: absolute;
		inset: 12px 0 auto;
		height: 1px;
		will-change: transform;
	}

	.fill {
		background: rgb(165 143 255 / 0.5);
		transform-origin: left;
		transform: scaleX(var(--p, 0));
	}

	/* A streak brightening to a lit tip at the band's left edge; the band
	   slides right with the fill so the tip leads it, fading in as it leaves
	   the dot and out as it reaches the next one. */
	.head {
		transform: translateX(calc(var(--p, 0) * 100%));
		opacity: min(1, calc(var(--p, 0) / 0.08), calc((1 - var(--p, 0)) / 0.12));
	}
	.head::before,
	.head::after {
		content: '';
		position: absolute;
		right: 100%;
	}
	.head::before {
		top: 0;
		width: 72px;
		height: 1px;
		background: linear-gradient(90deg, transparent, var(--color-accent-light));
	}
	.head::after {
		top: -2px;
		width: 5px;
		height: 5px;
		margin-right: -3px;
		border-radius: 9999px;
		background: #fff;
		box-shadow:
			0 0 6px 1px rgb(165 143 255 / 0.9),
			0 0 16px 4px rgb(124 92 255 / 0.45);
	}

	.tracing .dot {
		background: color-mix(
			in srgb,
			var(--color-accent-light) calc(var(--d, 0) * 100%),
			rgb(255 255 255 / 0.2)
		);
		box-shadow:
			0 0 0 3px rgb(165 143 255 / calc(var(--d, 0) * 0.14)),
			0 0 12px rgb(124 92 255 / calc(var(--d, 0) * 0.6));
	}
</style>
