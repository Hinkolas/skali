<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import { api, ApiError } from '$lib/api/client';
	import type { StageValuesResult } from '$lib/types/values';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';
	import SettingsNav from '$lib/components/project/SettingsNav.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// Pending edits by variable name. Plain values track any change (empty
	// string means unset, the dotenv rule); secrets are write-only, so a row
	// is dirty only once something was typed or explicitly unset.
	let dirty = $state<Record<string, string>>({});
	let saving = $state(false);
	let errorMessage = $state('');

	const declared = $derived(data.definition?.requiredVariables ?? []);
	const entryByName = $derived(new Map(data.values.map((v) => [v.name, v])));
	const declaredNames = $derived(new Set(declared.map((d) => d.name)));
	const undeclaredEntries = $derived(data.values.filter((v) => !declaredNames.has(v.name)));
	const dirtyCount = $derived(Object.keys(dirty).length);

	function editPlain(name: string, next: string, current: string) {
		if (next === current) {
			delete dirty[name];
		} else {
			dirty[name] = next;
		}
	}

	async function save() {
		if (!data.env || dirtyCount === 0) return;
		saving = true;
		errorMessage = '';
		try {
			await api.put<StageValuesResult>(`/v1/environments/${data.env.id}/values`, {
				values: { ...dirty }
			});
			toast.success('Values staged', {
				description: 'They apply with the next deployment.'
			});
			dirty = {};
			await invalidateAll();
		} catch (err) {
			errorMessage = err instanceof ApiError ? err.message : 'Could not stage the values.';
		} finally {
			saving = false;
		}
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
{:else if declared.length === 0 && undeclaredEntries.length === 0}
	<EmptyState
		icon={KeyRound}
		title="No variables declared"
		description="declare variables in skali.yaml; they become editable here"
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
					environment {data.env.name} · staged values apply with the next deployment
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
							<div class="mt-0.5 flex items-center gap-1.5">
								{#if variable.secret}
									<Pill text="secret" tone="warning" />
								{/if}
								{#if variable.required && !variable.hasDefault}
									<Pill text="required" tone="neutral" />
								{/if}
								{#if variable.description}
									<span class="text-text-ghost truncate text-xs" title={variable.description}>
										{variable.description}
									</span>
								{/if}
							</div>
						</div>
						{#if variable.secret}
							<div class="flex min-w-0 flex-1 items-center gap-2">
								<TextInput
									type="password"
									autocomplete="off"
									mono
									placeholder={entry
										? `set · v${entry.version} · type to overwrite`
										: 'not set · type to set'}
									value={pending === '' ? '' : (pending ?? '')}
									oninput={(e) => {
										const next = (e.currentTarget as HTMLInputElement).value;
										if (next === '') delete dirty[variable.name];
										else dirty[variable.name] = next;
									}}
								/>
								{#if entry && pending === undefined}
									<Button size="sm" variant="ghost" onclick={() => (dirty[variable.name] = '')}>
										Unset
									</Button>
								{:else if pending === ''}
									<span class="text-status-warning flex-none text-md">will unset</span>
									<Button size="sm" variant="ghost" onclick={() => delete dirty[variable.name]}>
										Keep
									</Button>
								{/if}
							</div>
						{:else}
							<div class="flex min-w-0 flex-1 items-center gap-2">
								<TextInput
									mono
									placeholder={variable.hasDefault ? `default: ${variable.default}` : 'not set'}
									value={pending ?? entry?.value ?? ''}
									oninput={(e) =>
										editPlain(
											variable.name,
											(e.currentTarget as HTMLInputElement).value,
											entry?.value ?? ''
										)}
								/>
								{#if pending !== undefined}
									<span class="text-status-warning flex-none text-md">
										{pending === '' ? 'will unset' : 'edited'}
									</span>
								{/if}
							</div>
						{/if}
					</div>
				{/each}
			</div>
		</Card>

		{#if undeclaredEntries.length > 0}
			<Card class="p-5">
				<div class="mb-1 flex items-baseline gap-2.5">
					<h3 class="text-text-primary text-xl font-semibold">Not declared</h3>
					<span class="text-text-ghost text-md">
						stored but absent from the current draft; unused until declared again
					</span>
				</div>
				<div class="flex flex-col">
					{#each undeclaredEntries as entry (entry.name)}
						<div
							class="border-border-subtle flex items-center gap-3 border-b py-2.75 last:border-0"
						>
							<span class="font-mono text-text-primary text-md">{entry.name}</span>
							{#if entry.secret}
								<Pill text="secret" tone="warning" />
							{/if}
							<span class="font-mono text-text-ghost ml-auto truncate text-xs">
								{entry.secret ? `v${entry.version}` : (entry.value ?? '')}
							</span>
						</div>
					{/each}
				</div>
			</Card>
		{/if}
	</div>
{/if}
