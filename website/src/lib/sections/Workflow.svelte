<script lang="ts">
	import SectionHeader from '$lib/components/SectionHeader.svelte';

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
</script>

<section id="workflow" class="mx-auto flex max-w-330 scroll-mt-6 flex-col gap-16 px-6 py-35">
	<!-- As in Platform: the timeline runs wider so each command fits on one
	     line, while the header keeps the measure of the other sections. -->
	<div class="mx-auto w-full max-w-288">
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
	<!-- Where scroll-driven animation is supported, a trace runs along the rule
	     as the timeline scrolls up the screen and lights each step's dot as it
	     arrives. Elsewhere, and with reduced motion, the dots are simply lit. -->
	<ol class="m-0 grid list-none gap-y-10 p-0 md:grid-cols-2 xl:grid-cols-4">
		{#each steps as step, i (step.label)}
			<li class="relative flex flex-col gap-3.5 border-t border-white/10 pt-8 pr-7" style:--i={i}>
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

	@supports (animation-timeline: view()) {
		@media (prefers-reduced-motion: no-preference) {
			ol {
				view-timeline: --workflow block;
			}

			/* A 25px band centred on the 1px rule, clipped so the head never
			   reaches past its own step. */
			.trace {
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
			}

			.fill {
				background: rgb(165 143 255 / 0.5);
				transform-origin: left;
				transform: scaleX(0);
			}

			/* A streak brightening to a lit tip at the band's left edge; the
			   band slides right with the fill so the tip leads it. */
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

			.dot {
				background: rgb(255 255 255 / 0.2);
			}

			/* Each step takes its turn in one stretch of the timeline's
			   passage, so the trace reads as a single line across a row. */
			.fill,
			.head,
			.dot {
				animation-timing-function: linear;
				animation-fill-mode: both;
				animation-timeline: --workflow;
				animation-range: cover calc(12% + var(--i) * 8.25%) cover calc(20.25% + var(--i) * 8.25%);
			}
			.fill {
				animation-name: fill;
			}
			.head {
				animation-name: head;
			}
			.dot {
				animation-name: arrive;
				animation-range: cover calc(12% + var(--i) * 8.25%) cover calc(13.5% + var(--i) * 8.25%);
			}

			/* One column: each step follows its own way up the screen. */
			@media (width < 48rem) {
				.fill,
				.head,
				.dot {
					animation-timeline: view();
					animation-range: cover 22% cover 46%;
				}
				.dot {
					animation-range: cover 22% cover 25%;
				}
			}
		}
	}

	@keyframes fill {
		to {
			transform: scaleX(1);
		}
	}

	@keyframes head {
		from {
			transform: translateX(0);
			opacity: 0;
		}
		8%,
		88% {
			opacity: 1;
		}
		to {
			transform: translateX(100%);
			opacity: 0;
		}
	}

	@keyframes arrive {
		from {
			background: rgb(255 255 255 / 0.2);
			box-shadow: 0 0 0 0 rgb(165 143 255 / 0);
		}
		to {
			background: var(--color-accent-light);
			box-shadow:
				0 0 0 3px rgb(165 143 255 / 0.14),
				0 0 12px rgb(124 92 255 / 0.6);
		}
	}
</style>
