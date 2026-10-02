<script lang="ts">
	import { resolve } from '$app/paths';
	import Database from '@lucide/svelte/icons/database';
	import { formatBytes, formatDateTime, relativeTime } from '$lib/format';
	import { withEnv } from '$lib/urls';
	import { databasePhase, type DatabasePoolDatabase } from '$lib/types/pools';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';
	import type { PageData } from './$types';

	// Every logical database on the pool with who owns it and how big it
	// is. Project databases link to their project and service pages; the
	// platform's own show as system.
	let { data }: { data: PageData } = $props();

	const grid = 'grid-cols-[1.6fr_1.4fr_1.1fr_1fr_1fr_0.8fr]';

	// Project databases first (by project, environment, name), system last.
	const rows = $derived(
		data.pool.databases.toSorted(
			(a, b) =>
				Number(a.owner === 'system') - Number(b.owner === 'system') ||
				(a.project?.name ?? '').localeCompare(b.project?.name ?? '') ||
				(a.environment?.name ?? '').localeCompare(b.environment?.name ?? '') ||
				a.database_name.localeCompare(b.database_name)
		)
	);

	function projectHref(database: DatabasePoolDatabase): string | null {
		if (!database.project) return null;
		return withEnv(
			resolve('/(app)/projects/[project]', { project: database.project.name }),
			database.environment?.name
		);
	}
	function serviceHref(database: DatabasePoolDatabase): string | null {
		if (!database.project || !database.service_key) return null;
		return withEnv(
			resolve('/(app)/projects/[project]/services/[service]', {
				project: database.project.name,
				service: database.service_key
			}),
			database.environment?.name
		);
	}
</script>

<svelte:head>
	<title>Databases · {data.pool.name} — skali</title>
</svelte:head>

<div class="pb-6">
	{#if rows.length > 0}
		<Table columns={['Database', 'Project', 'Service', 'Status', 'Size', 'Created']} {grid}>
			{#each rows as database (database.database_name)}
				{@const phase = databasePhase(database.phase)}
				{@const project = projectHref(database)}
				{@const service = serviceHref(database)}
				<div
					class="border-border-subtle border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 @max-2xl:flex @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-3 @max-2xl:gap-y-2 @2xl:grid @2xl:items-center {grid}"
				>
					<div
						class="text-text-primary truncate font-mono text-md @max-2xl:flex-1"
						title="role {database.role_name}"
					>
						{database.database_name}
					</div>
					<div
						class="@2xl:contents @max-2xl:order-1 @max-2xl:flex @max-2xl:basis-full @max-2xl:flex-wrap @max-2xl:items-center @max-2xl:gap-x-4 @max-2xl:gap-y-1.5"
					>
						<div class="flex min-w-0 items-center gap-2 pr-3">
							{#if database.project && project}
								<!-- eslint-disable svelte/no-navigation-without-resolve -- resolved path, env appended by $lib/urls -->
								<a href={project} class="text-text-secondary truncate text-md hover:underline">
									{database.project.display_name || database.project.name}
								</a>
								<!-- eslint-enable svelte/no-navigation-without-resolve -->
								{#if database.environment}
									<Pill text={database.environment.name} />
								{/if}
							{:else}
								<span class="text-text-muted text-md">system</span>
							{/if}
						</div>
						<div class="flex min-w-0 items-center gap-2 pr-3">
							{#if service}
								<TypeBadge kind="database" />
								<!-- eslint-disable svelte/no-navigation-without-resolve -- resolved path, env appended by $lib/urls -->
								<a
									href={service}
									class="text-text-secondary truncate font-mono text-md hover:underline"
								>
									{database.service_key}
								</a>
								<!-- eslint-enable svelte/no-navigation-without-resolve -->
							{:else}
								<span class="text-text-muted truncate font-mono text-md">
									{database.system_key ?? database.role_name}
								</span>
							{/if}
						</div>
						<div><Pill text={phase.text} tone={phase.tone} /></div>
						<div class="flex flex-col pr-3">
							<span class="text-text-secondary font-mono text-md">
								{database.used_bytes == null ? 'not measured' : formatBytes(database.used_bytes)}
							</span>
							<span class="text-text-faint font-mono text-xs">
								of {formatBytes(database.storage_bytes)}
							</span>
						</div>
					</div>
					<div
						class="text-text-faint font-mono text-xs @max-2xl:ml-auto"
						title={formatDateTime(database.created_at)}
					>
						{relativeTime(database.created_at)}
					</div>
				</div>
			{/each}
		</Table>
	{:else}
		<EmptyState
			icon={Database}
			title="No databases on this pool"
			description="the first declared database on this pool appears here"
		/>
	{/if}
</div>
