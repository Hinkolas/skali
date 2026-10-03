<script lang="ts">
	// A deployment run in the Studio's run panel. The page renders it part way
	// through its rollout; once it scrolls into view it plays on as Studio
	// would show it: the remaining steps settle, the run succeeds and its bar
	// goes away, and after a pause the next run starts. Step titles are the
	// ones the controller reports.
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleDashed from '@lucide/svelte/icons/circle-dashed';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import { onMount } from 'svelte';
	import { cubicOut } from 'svelte/easing';
	import { fade, scale } from 'svelte/transition';

	type State = 'succeeded' | 'running' | 'pending';

	// The run as a sequence of leaf steps. `seconds` is what the step's clock
	// reads once it settles, `ms` how long it takes on the page.
	const leaves = [
		{ title: 'Validate project definition', seconds: 1, ms: 600 },
		{ title: 'Prepare artifacts', seconds: 42, ms: 2400 },
		{ title: 'Create revision', seconds: 1, ms: 500 },
		{ title: 'Provision databases.main', seconds: 12, ms: 1400, child: true },
		{ title: 'Issue TLS certificate for web / main', seconds: 8, ms: 1200, child: true },
		{ title: 'Roll out web', seconds: 9, ms: 1500, child: true },
		{ title: 'Verify health', seconds: 6, ms: 1100, child: true },
		{ title: 'Activate revision', seconds: 1, ms: 500, child: true }
	].map((leaf, i, all) => ({
		...leaf,
		start: all.slice(0, i).reduce((sum, l) => sum + l.ms, 0)
	}));

	const runtime = leaves.reduce((sum, l) => sum + l.ms, 0);
	const hold = 5200; // how long a finished run stays on screen
	const commits = ['3f9a1c2e', 'b71d04a9', 'e2c58f13', '9d4a7b60'];

	// Where the page starts: rolling out web, four seconds in.
	const rollOutWeb = leaves[5];
	const snapshot = rollOutWeb.start + rollOutWeb.ms * 0.5;

	let t = $state(snapshot);
	let run = $state(0);

	function leafAt(leaf: (typeof leaves)[number], at: number) {
		if (at < leaf.start) return { state: 'pending' as State, seconds: 0 };
		if (at >= leaf.start + leaf.ms) return { state: 'succeeded' as State, seconds: leaf.seconds };
		const seconds = Math.floor(((at - leaf.start) / leaf.ms) * leaf.seconds);
		return { state: 'running' as State, seconds };
	}

	function format(seconds: number) {
		if (seconds < 60) return `${seconds}s`;
		return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`;
	}

	const view = $derived.by(() => {
		const settled = leaves.map((leaf) => ({ ...leaf, ...leafAt(leaf, t) }));
		const children = settled.filter((s) => s.child);
		const started = children.filter((s) => s.state !== 'pending').length;
		const parent = {
			title: 'Roll out revision',
			state: (children.every((s) => s.state === 'succeeded')
				? 'succeeded'
				: started > 0
					? 'running'
					: 'pending') as State,
			seconds: children.reduce((sum, s) => sum + s.seconds, 0),
			progress: `${started}/${children.length}`,
			child: false
		};
		const top = [...settled.filter((s) => !s.child), parent];
		const done = top.filter((s) => s.state === 'succeeded').length;
		return {
			rows: [...top, ...children],
			elapsed: format(settled.reduce((sum, s) => sum + s.seconds, 0)),
			// As in Studio: settled top-level steps fill the bar, and an active
			// bar always keeps a sliver for its sheen.
			progress: Math.max((done / top.length) * 100, 3),
			succeeded: done === top.length
		};
	});

	let root: HTMLDivElement;

	onMount(() => {
		if (matchMedia('(prefers-reduced-motion: reduce)').matches) return;

		let visible = false;
		let frame = 0;
		let last = 0;

		const tick = (now: number) => {
			t += now - last;
			last = now;
			if (t >= runtime + hold) {
				t = 0;
				run += 1;
			}
			frame = requestAnimationFrame(tick);
		};

		// Only play while someone can see it.
		const update = () => {
			const play = visible && !document.hidden;
			if (play && !frame) {
				last = performance.now();
				frame = requestAnimationFrame(tick);
			} else if (!play && frame) {
				cancelAnimationFrame(frame);
				frame = 0;
			}
		};

		const observer = new IntersectionObserver(
			([entry]) => {
				visible = entry.isIntersecting;
				update();
			},
			{ threshold: 0.35 }
		);
		observer.observe(root);
		document.addEventListener('visibilitychange', update);

		return () => {
			observer.disconnect();
			document.removeEventListener('visibilitychange', update);
			cancelAnimationFrame(frame);
		};
	});
</script>

<div
	bind:this={root}
	class="w-full overflow-hidden rounded-[15px] border border-border-raised bg-surface-card leading-normal text-text-primary"
>
	{#key run}
		<div in:fade={{ duration: 450 }}>
			<div class="flex flex-col border-b border-border-subtle px-[19.8px] pt-[17.6px] pb-[15px]">
				<div class="flex items-baseline gap-2.5">
					<span class="text-[17px] font-semibold">deployment</span>
					<span
						class={[
							'text-[13px] font-medium transition-colors duration-300',
							view.succeeded ? 'text-status-success' : 'text-status-warning'
						]}
					>
						{view.succeeded ? 'succeeded' : 'running'}
					</span>
				</div>
				<div class="mt-1.5 truncate font-mono text-[11px] text-text-faint">
					{commits[run % commits.length]} · Nicholas Hinke
					<span class="text-text-ghost">(you)</span> · {view.elapsed}
				</div>
				<!-- Studio drops the bar once the run settles; here it folds away. -->
				<div
					class={[
						'grid transition-[grid-template-rows,opacity] duration-500 ease-out',
						view.succeeded ? 'grid-rows-[0fr] opacity-0 delay-500' : 'grid-rows-[1fr]'
					]}
				>
					<div class="min-h-0 overflow-hidden">
						<div class="mt-2.5 h-[4.4px] overflow-hidden rounded-full bg-white/6">
							<div
								class="relative h-full overflow-hidden rounded-full bg-status-warning transition-[width] duration-500 ease-out"
								style:width="{view.progress}%"
							>
								<div
									class="absolute inset-0 bg-linear-to-r from-transparent via-white/30 to-transparent motion-safe:animate-progress-sheen"
								></div>
							</div>
						</div>
					</div>
				</div>
			</div>
			<div class="flex flex-col px-3 pt-2 pb-3 text-[14px]">
				{#each view.rows as step (step.title)}
					<div
						class={[
							'flex items-center gap-[11px] rounded-[9px] py-[7.7px] pr-[8.8px] transition-colors duration-300',
							step.child ? 'pl-[26.8px]' : 'pl-[8.8px]',
							step.state === 'running' && !step.child && 'bg-white/3'
						]}
					>
						{#if step.state === 'succeeded'}
							<span
								class="flex shrink-0"
								in:scale={{ start: 0.4, duration: 260, easing: cubicOut }}
							>
								<CircleCheck size={15} class="text-status-success" />
							</span>
						{:else if step.state === 'running'}
							<LoaderCircle
								size={15}
								class="shrink-0 text-status-warning motion-safe:animate-spin"
							/>
						{:else}
							<CircleDashed size={15} class="shrink-0 text-text-ghost" />
						{/if}
						<span
							class={[
								'grow truncate transition-colors duration-300',
								step.state === 'running' && 'text-text-primary',
								step.state === 'succeeded' && 'text-text-secondary',
								step.state === 'pending' && 'text-text-faint'
							]}
						>
							{step.title}
						</span>
						{#if 'progress' in step}
							<span class="font-mono text-[11px] text-text-faint">{step.progress}</span>
						{/if}
						{#if step.state !== 'pending'}
							<span class="font-mono text-[11px] text-text-ghost">{format(step.seconds)}</span>
						{/if}
					</div>
				{/each}
			</div>
		</div>
	{/key}
</div>
