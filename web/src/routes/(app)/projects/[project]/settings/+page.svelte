<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import Plus from '@lucide/svelte/icons/plus';
	import { api, ApiError } from '$lib/api/client';
	import { formatDateTime, relativeTime } from '$lib/format';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { modal } from '$lib/stores/modal.svelte';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import Field from '$lib/components/ui/Field.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';
	import RunDetailPanel from '$lib/components/run/RunDetailPanel.svelte';
	import DeleteProjectModal, {
		modalOptions as deleteProjectModalOptions
	} from '$lib/components/project/DeleteProjectModal.svelte';
	import NewEnvironmentModal, {
		modalOptions as newEnvironmentModalOptions
	} from '$lib/components/project/NewEnvironmentModal.svelte';
	import SettingsNav from '$lib/components/project/SettingsNav.svelte';
	import type { Target } from '$lib/types/revisions';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// Writable derived: resets to the loaded value whenever the project data
	// changes (save, env switch), while staying editable in between.
	let displayName = $derived(data.project.display_name);
	let savingName = $state(false);

	async function saveDisplayName() {
		savingName = true;
		try {
			await api.patch(`/v1/projects/${data.project.id}`, { display_name: displayName.trim() });
			toast.success('Display name updated');
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not update the project');
		} finally {
			savingName = false;
		}
	}

	function teardown(envId: string, envName: string, purge: boolean) {
		dialog.confirm({
			title: purge ? `Purge ${envName}?` : `Tear down ${envName}?`,
			description: purge
				? 'Destroys the namespace with its volumes and deletes the environment with all values, revisions, and history. This is one-way.'
				: 'Removes the running workloads but keeps values, revisions, and volumes. The next deployment resurrects the environment.',
			confirmLabel: purge ? 'Purge environment' : 'Tear down',
			variant: 'danger',
			onConfirm: async () => {
				try {
					const result = await api.post<{ run_id: string; purge: boolean }>(
						`/v1/environments/${envId}/teardown`,
						purge ? { purge: true } : undefined
					);
					toast.success(purge ? 'Purge started' : 'Teardown started');
					sidepanel.open(RunDetailPanel, { runId: result.run_id }, { label: 'Run details' });
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not start the teardown');
					throw err;
				}
			}
		});
	}

	function rollback(revisionID: string) {
		if (!data.env) return;
		const envId = data.env.id;
		dialog.confirm({
			title: 'Roll back to this revision?',
			description:
				'Points the environment at the selected revision; reconciliation applies it as a journaled run.',
			confirmLabel: 'Roll back',
			onConfirm: async () => {
				try {
					const result = await api.put<{ target: Target; run_id: string }>(
						`/v1/environments/${envId}/target`,
						{ revision_id: revisionID }
					);
					toast.success('Rollback started');
					sidepanel.open(RunDetailPanel, { runId: result.run_id }, { label: 'Run details' });
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not roll back');
					throw err;
				}
			}
		});
	}
</script>

<svelte:head>
	<title>Settings · {data.project.display_name || data.project.name} — skali</title>
</svelte:head>

<PageHeader title="Settings">
	{#snippet subtitle()}
		{data.project.name} · project configuration
	{/snippet}
</PageHeader>

<SettingsNav project={data.project} />

<div class="flex max-w-3xl flex-col gap-3.5 pb-6">
	<Card class="p-5">
		<h3 class="text-text-primary mb-3.5 text-xl font-semibold">General</h3>
		<div class="flex flex-col gap-3.5">
			<Field label="Project name" description="Stable identity; matches the manifest name.">
				<TextInput value={data.project.name} mono disabled />
			</Field>
			<Field
				label="Display name"
				description="Shown in lists and headers; empty falls back to the name."
			>
				<div class="flex gap-2">
					<TextInput bind:value={displayName} placeholder={data.project.name} />
					<Button
						busy={savingName}
						disabled={displayName === data.project.display_name}
						onclick={saveDisplayName}
					>
						Save
					</Button>
				</div>
			</Field>
		</div>
	</Card>

	<Card class="p-5">
		<div class="mb-3.5 flex items-center">
			<h3 class="text-text-primary text-xl font-semibold">Environments</h3>
			<div class="ml-auto">
				<Button
					size="sm"
					onclick={() =>
						modal.open(NewEnvironmentModal, { project: data.project }, newEnvironmentModalOptions)}
				>
					<Plus size={14} /> New environment
				</Button>
			</div>
		</div>
		<div class="flex flex-col">
			{#each data.environments as environment (environment.id)}
				<div class="border-border-subtle flex items-center gap-3 border-b py-2.75 last:border-0">
					<span class="font-mono text-text-primary text-md">{environment.name}</span>
					{#if environment.id === data.env?.id}
						<Pill text="current" tone="success" />
					{/if}
					<span class="font-mono text-text-ghost text-xs">
						created {relativeTime(environment.created_at)}
					</span>
					<div class="ml-auto flex gap-2">
						<Button
							size="sm"
							variant="ghost"
							onclick={() => teardown(environment.id, environment.name, false)}
						>
							Tear down
						</Button>
						<Button
							size="sm"
							variant="danger"
							onclick={() => teardown(environment.id, environment.name, true)}
						>
							Purge
						</Button>
					</div>
				</div>
			{:else}
				<div class="font-mono text-text-ghost py-2 text-xs">no environments yet</div>
			{/each}
		</div>
	</Card>

	<Card class="p-5">
		<div class="mb-3.5 flex items-baseline gap-2.5">
			<h3 class="text-text-primary text-xl font-semibold">Revisions</h3>
			{#if data.env}
				<span class="text-text-ghost text-md">environment {data.env.name}</span>
			{/if}
		</div>
		<div class="flex flex-col">
			{#each data.revisions as revision (revision.id)}
				{@const isTarget = revision.id === data.target?.target_revision_id}
				{@const isActive = revision.id === data.target?.active_revision_id}
				<div class="border-border-subtle flex items-center gap-3 border-b py-2.75 last:border-0">
					<span class="font-mono text-text-primary text-md" title={revision.id}>
						{revision.checksum.slice(0, 10)}
					</span>
					{#if isTarget}
						<Pill text="target" tone="success" />
					{/if}
					{#if isActive && !isTarget}
						<Pill text="active" tone="neutral" />
					{/if}
					<span
						class="font-mono text-text-ghost text-xs"
						title={formatDateTime(revision.created_at)}
					>
						{relativeTime(revision.created_at)}
					</span>
					<div class="ml-auto">
						{#if !isTarget}
							<Button size="sm" variant="ghost" onclick={() => rollback(revision.id)}>
								Roll back
							</Button>
						{/if}
					</div>
				</div>
			{:else}
				<div class="font-mono text-text-ghost py-2 text-xs">
					no revisions yet · the first deploy creates one
				</div>
			{/each}
		</div>
	</Card>

	<Card class="border-status-danger/30 p-5">
		<div class="flex items-center">
			<div>
				<h3 class="text-text-primary text-xl font-semibold">Danger zone</h3>
				<div class="text-text-muted mt-1 text-base">
					Deleting a project removes its environments, values, revisions, and history.
				</div>
			</div>
			<div class="ml-auto">
				<Button
					variant="danger"
					onclick={() =>
						modal.open(DeleteProjectModal, { project: data.project }, deleteProjectModalOptions)}
				>
					Delete project
				</Button>
			</div>
		</div>
	</Card>
</div>
