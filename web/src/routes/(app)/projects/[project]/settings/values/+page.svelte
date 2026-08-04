<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import { api, ApiError } from '$lib/api/client';
	import type { StageValuesResult } from '$lib/types/values';
	import { toast } from '$lib/stores/toast.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';
	import SettingsNav from '$lib/components/project/SettingsNav.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// Pending edits by variable name. Values are write-only, so a row is
	// dirty only once something was typed or explicitly set empty; clearing
	// the input reverts the row to "no edit". The empty string is a real
	// value and stages like any other.
	let dirty = $state<Record<string, string>>({});
	let saving = $state(false);
	let errorMessage = $state('');

	// Runtime variables are the storable contract; build-only variables
	// resolve from a local env file at deploy time and have no stored row.
	const declared = $derived(
		(data.definition?.requiredVariables ?? []).filter((v) => v.runtime || !v.build)
	);
	const entryByName = $derived(new Map(data.values.map((v) => [v.name, v])));
	const declaredNames = $derived(new Set(declared.map((d) => d.name)));
	const orphanedEntries = $derived(data.values.filter((v) => !declaredNames.has(v.name)));
	const dirtyCount = $derived(Object.keys(dirty).length);

	async function save() {
		if (!data.env || dirtyCount === 0) return;
		saving = true;
		errorMessage = '';
		try {
			const res = await api.put<StageValuesResult>(`/v1/environments/${data.env.id}/values`, {
				values: { ...dirty }
			});
			toast.success('Values staged', {
				description: 'They apply with the next deployment.'
			});
			if (res.skipped.length > 0) {
				toast.warning(`Skipped ${res.skipped.join(', ')}`, {
					description: 'Not referenced by the current draft.'
				});
			}
			dirty = {};
			await invalidateAll();
		} catch (err) {
			errorMessage = err instanceof ApiError ? err.message : 'Could not stage the values.';
		} finally {
			saving = false;
		}
	}

	function unsetValue(name: string, required: boolean) {
		let description =
			'Removes the stored value immediately. Running revisions keep the value they ' +
			'pinned; the next deployment will no longer include it.';
		if (required) {
			description += ` ${name} is required by the manifest; the next deployment will fail until it is set again.`;
		}
		dialog.confirm({
			title: `Unset ${name}?`,
			description,
			confirmLabel: 'Unset',
			variant: 'danger',
			onConfirm: async () => {
				if (!data.env) return;
				await api.del(`/v1/environments/${data.env.id}/values/${name}`);
				toast.success('Value unset');
				await invalidateAll();
			}
		});
	}

	function deleteOrphan(name: string) {
		dialog.confirm({
			title: `Delete ${name}?`,
			description:
				'Removes the stored value immediately. It is not referenced by the current draft.',
			confirmLabel: 'Delete',
			variant: 'danger',
			onConfirm: async () => {
				if (!data.env) return;
				await api.del(`/v1/environments/${data.env.id}/values/${name}`);
				toast.success('Value deleted');
				await invalidateAll();
			}
		});
	}
</script>

<svelte:head>
	<title>Values · {data.project.display_name || data.project.name} — skali</title>
</svelte:head>

<PageHeader title="Settings">
	{#snippet subtitle()}
		{data.project.name} · environment values
	{/snippet}
	{#snippet actions()}
		<Button variant="primary" busy={saving} disabled={dirtyCount === 0} onclick={save}>
			Stage {dirtyCount > 0 ? dirtyCount : ''} change{dirtyCount === 1 ? '' : 's'}
		</Button>
	{/snippet}
</PageHeader>

<SettingsNav project={data.project} />

{#if !data.env}
	<EmptyState
		icon={KeyRound}
		title="No environment"
		description="create an environment first; values are stored per environment"
	/>
{:else if declared.length === 0 && orphanedEntries.length === 0}
	<EmptyState
		icon={KeyRound}
		title="No variables referenced"
		description="reference values with $&lbrace;VAR&rbrace; in skali.yaml; they become editable here"
	/>
{:else}
	<div class="flex max-w-3xl flex-col gap-3.5 pb-6">
		{#if errorMessage}
			<div
				class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[11px] border px-4 py-3 text-base"
			>
				{errorMessage}
			</div>
		{/if}

		<Card class="p-5">
			<div class="mb-1 flex items-baseline gap-2.5">
				<h3 class="text-text-primary text-xl font-semibold">Variables</h3>
				<span class="text-text-ghost text-md">
					environment {data.env.name} · write-only · staged values apply with the next deployment
				</span>
			</div>
			<div class="flex flex-col">
				{#each declared as variable (variable.name)}
					{@const entry = entryByName.get(variable.name)}
					{@const pending = dirty[variable.name]}
					<div class="border-border-subtle flex items-center gap-3 border-b py-3 last:border-0">
						<div class="w-56 flex-none">
							<div class="font-mono text-text-primary truncate text-md" title={variable.name}>
								{variable.name}
							</div>
							{#if variable.required && !variable.hasDefault}
								<div class="mt-0.5 flex items-center gap-1.5">
									<Pill text="required" tone="neutral" />
								</div>
							{/if}
						</div>
						<div class="flex min-w-0 flex-1 items-center gap-2">
							<TextInput
								type="password"
								autocomplete="off"
								mono
								placeholder={entry
									? `set · v${entry.version} · type to overwrite`
									: variable.hasDefault
										? `not set · default "${variable.default ?? ''}" applies`
										: 'not set · type to set'}
								value={pending ?? ''}
								oninput={(e) => {
									const next = (e.currentTarget as HTMLInputElement).value;
									if (next === '') delete dirty[variable.name];
									else dirty[variable.name] = next;
								}}
							/>
							{#if pending === ''}
								<span class="text-status-warning flex-none text-md">will set empty</span>
								<Button size="sm" variant="ghost" onclick={() => delete dirty[variable.name]}>
									Keep
								</Button>
							{:else if pending === undefined}
								<Button size="sm" variant="ghost" onclick={() => (dirty[variable.name] = '')}>
									Set empty
								</Button>
								{#if entry}
									<Button
										size="sm"
										variant="ghost"
										onclick={() =>
											unsetValue(variable.name, variable.required && !variable.hasDefault)}
									>
										Unset
									</Button>
								{/if}
							{/if}
						</div>
					</div>
				{/each}
			</div>
		</Card>

		{#if orphanedEntries.length > 0}
			<Card class="p-5">
				<div class="mb-1 flex items-baseline gap-2.5">
					<h3 class="text-text-primary text-xl font-semibold">No longer referenced</h3>
					<span class="text-text-ghost text-md">
						stored but not referenced by the current draft; ignored by deployments
					</span>
				</div>
				<div class="flex flex-col">
					{#each orphanedEntries as entry (entry.name)}
						<div
							class="border-border-subtle flex items-center gap-3 border-b py-2.75 last:border-0"
						>
							<span class="font-mono text-text-primary text-md">{entry.name}</span>
							<span class="font-mono text-text-ghost text-xs">v{entry.version}</span>
							<div class="ml-auto">
								<Button size="sm" variant="ghost" onclick={() => deleteOrphan(entry.name)}>
									Delete
								</Button>
							</div>
						</div>
					{/each}
				</div>
			</Card>
		{/if}
	</div>
{/if}
