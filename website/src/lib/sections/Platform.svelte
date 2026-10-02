<script lang="ts">
	import SectionHeader from '$lib/components/SectionHeader.svelte';
	import Backups from '$lib/mock/Backups.svelte';
	import BucketConnection from '$lib/mock/BucketConnection.svelte';
	import DatabaseInstance from '$lib/mock/DatabaseInstance.svelte';
	import Promote from '$lib/mock/Promote.svelte';
	import RunSteps from '$lib/mock/RunSteps.svelte';
</script>

{#snippet facts(items: string[])}
	<ul
		class="m-0 flex list-none flex-col border-t border-border-section p-0 text-[15px] text-text-tertiary"
	>
		{#each items as item (item)}
			<li class="border-b border-border-section py-3.5">{item}</li>
		{/each}
	</ul>
{/snippet}

{#snippet copy(eyebrow: string, color: string, title: string, text: string, items: string[])}
	<div class="flex max-w-125 flex-[1_1_400px] flex-col gap-5">
		<span class="font-mono text-[13px] {color}">{eyebrow}</span>
		<h3 class="m-0 text-[clamp(28px,3.2vw,40px)] leading-[1.1] font-medium tracking-[-0.03em]">
			{title}
		</h3>
		<p class="m-0 text-lg leading-[1.65] text-text-muted">{text}</p>
		{@render facts(items)}
	</div>
{/snippet}

<section id="platform" class="mx-auto flex max-w-330 scroll-mt-6 flex-col gap-30 px-6 py-35">
	<!-- The rows run wider than the other sections; the header keeps their
	     measure so its label lines up with theirs. -->
	<div class="mx-auto w-full max-w-288">
		<SectionHeader
			index="02"
			label="Platform"
			title="Everything you would otherwise script yourself."
		/>
	</div>

	<div class="flex flex-wrap items-center gap-x-20 gap-y-12">
		{@render copy(
			'Deployments',
			'text-service-app',
			'Every deploy is a run you can follow.',
			'Artifacts, migrations, certificates and health checks happen in order, in the open. Blue-green is the default, so the old revision serves until the new one is healthy.',
			[
				'Rolling and recreate when you prefer them',
				'Release commands for migrations',
				'skali rollback to the revision before'
			]
		)}
		<div
			role="img"
			aria-label="A deployment run in Skali Studio, rolling out the web application"
			class="relative h-110 flex-[1_1_480px] overflow-hidden rounded-[22px] border border-border-default bg-surface-panel px-[clamp(20px,3vw,40px)] pt-[clamp(20px,3vw,40px)]"
			style="background-image: radial-gradient(520px 320px at 80% 0%, rgb(124 92 255 / 0.14) 0%, transparent 70%)"
		>
			<div class="fade-bottom"><RunSteps /></div>
		</div>
	</div>

	<div class="flex flex-wrap-reverse items-center gap-x-20 gap-y-12">
		<div
			role="img"
			aria-label="A PostgreSQL instance with the vector extension and a bucket's S3 connection in Skali Studio"
			class="grid min-w-0 flex-[1_1_480px] grid-cols-[repeat(auto-fit,minmax(min(300px,100%),1fr))] items-start gap-4 rounded-[22px] border border-border-default bg-surface-panel p-[clamp(20px,3vw,40px)] *:min-w-0"
			style="background-image: radial-gradient(520px 320px at 20% 100%, rgb(104 196 216 / 0.10) 0%, transparent 70%)"
		>
			<DatabaseInstance />
			<BucketConnection />
		</div>
		{@render copy(
			'Data',
			'text-service-db',
			'Postgres and S3, part of the app.',
			'Databases and buckets are declared next to the code that uses them, provisioned on deploy, and wired in as environment variables. Nobody copies credentials.',
			[
				'pgvector and other extensions on request',
				'Each bucket on its own hostname',
				'Shared, project or dedicated isolation'
			]
		)}
	</div>

	<div class="flex flex-wrap items-center gap-x-20 gap-y-12">
		{@render copy(
			'Operations',
			'text-service-storage',
			'Backups and promotions without a runbook.',
			'Snapshot an environment to your own S3 on a schedule, restore it in one step, and move a tested revision from staging to production without rebuilding it.',
			[
				'Cron schedules and retention per environment',
				'Protected environments accept promotions only',
				'skali deploy --from staging'
			]
		)}
		<div
			role="img"
			aria-label="A backup schedule with recent snapshots and the promote dialog in Skali Studio"
			class="relative flex flex-[1_1_480px] flex-col gap-4 overflow-hidden rounded-[22px] border border-border-default bg-surface-panel p-[clamp(20px,3vw,40px)] sm:block sm:h-115"
			style="background-image: radial-gradient(520px 320px at 80% 100%, rgb(217 165 79 / 0.10) 0%, transparent 70%)"
		>
			<div class="sm:w-[78%]"><Backups /></div>
			<div class="sm:absolute sm:right-8 sm:bottom-8 sm:w-[62%]"><Promote /></div>
		</div>
	</div>
</section>
