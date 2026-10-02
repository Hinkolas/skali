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
	<ol
		class="m-0 grid list-none grid-cols-[repeat(auto-fit,minmax(min(240px,100%),1fr))] gap-y-10 border-t border-white/10 p-0"
	>
		{#each steps as step (step.label)}
			<li class="relative flex flex-col gap-3.5 pt-8 pr-7">
				<span
					aria-hidden="true"
					class="absolute -top-1 left-0 size-[7px] rounded-full bg-accent-light"
				></span>
				<span class="font-mono text-xs text-text-faint">{step.label}</span>
				<code class="font-mono text-[17px] text-text-primary">{step.command}</code>
				<p class="m-0 text-[15px] leading-[1.6] text-text-muted">{step.text}</p>
			</li>
		{/each}
	</ol>
</section>
