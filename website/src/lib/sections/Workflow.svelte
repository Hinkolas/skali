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

	// The trace follows scroll, eased: scrolling sets where it should be, and
	// it glides there no faster than about two and a half steps a second, so even
	// a quick scroll shows the tip travelling from dot to dot. Per step, `--p`
	// is how far along its stretch of the rule the trace is and `--d` how lit
	// its dot is. Above one column the steps take turns over one long stretch
	// of the list's passage up the screen, so the trace reads as a single line
	// across a row; in one column each step follows its own passage.
	onMount(() => {
		if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;
		tracing = true;

		const items = [...list.children] as HTMLElement[];
		const single = matchMedia('(width < 48rem)');
		const clamp = (x: number, max = 1) => Math.min(max, Math.max(0, x));
		// 0 when the element's top enters at the bottom of the screen, 1 when
		// its bottom leaves at the top.
		const passage = (el: Element) => {
			const box = el.getBoundingClientRect();
			return (innerHeight - box.top) / (innerHeight + box.height);
		};
		const span = (at: number, from: number, to: number) => clamp((at - from) / (to - from));

		// Where the trace is: steps along the whole rule above one column, and
		// per step in one.
		let along = 0;
		const each = items.map(() => 0);

		const approach = (current: number, target: number, dt: number) => {
			const eased = (target - current) * (1 - Math.exp(-dt / 180));
			const cap = (2.5 * dt) / 1000;
			const next = current + Math.min(cap, Math.max(-cap, eased));
			return Math.abs(target - next) < 0.0005 ? target : next;
		};

		const paint = (i: number, p: number) => {
			items[i].style.setProperty('--p', p.toFixed(4));
			items[i].style.setProperty('--d', clamp(p / 0.15).toFixed(4));
		};

		let frame = 0;
		let last = 0;
		const tick = (now: number) => {
			const dt = Math.min(64, now - last);
			last = now;
			let settled = true;
			if (single.matches) {
				items.forEach((item, i) => {
					const target = span(passage(item), 0.15, 0.55);
					each[i] = approach(each[i], target, dt);
					settled &&= each[i] === target;
					paint(i, each[i]);
				});
			} else {
				const target = span(passage(list), 0.08, 0.62) * items.length;
				along = approach(along, target, dt);
				settled = along === target;
				items.forEach((_, i) => paint(i, clamp(along - i)));
			}
			frame = settled ? 0 : requestAnimationFrame(tick);
		};
		const schedule = () => {
			if (frame) return;
			last = performance.now();
			frame = requestAnimationFrame(tick);
		};

		schedule();
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
	   slides right with the fill so the tip leads it. It is lit only while
	   its step is under way, handing over to the next step's at the dot. */
	.head {
		transform: translateX(calc(var(--p, 0) * 100%));
		opacity: min(1, calc(var(--p, 0) / 0.02), calc((1 - var(--p, 0)) / 0.02));
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
