<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import KeyRound from '@lucide/svelte/icons/key-round';
	import Lock from '@lucide/svelte/icons/lock';
	import { api, ApiError } from '$lib/api/client';
	import { requiredTitle, roleAtLeast } from '$lib/access';
	import { resolve } from '$app/paths';
	import type { Expression } from '$lib/types/definition';
	import type { StageValuesResult } from '$lib/types/values';
	import { withEnv } from '$lib/urls';
	import { toast } from '$lib/stores/toast.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import Menu from '$lib/components/ui/Menu.svelte';
	import MenuItem from '$lib/components/ui/MenuItem.svelte';
	import SecretInput from '$lib/components/ui/SecretInput.svelte';
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
	const declared = $derived(data.definition?.requiredVariables ?? []);

	// Which applications read each variable: every ${VAR} part in an app's
	// environment or route domains. Saying who consumes a value tells the
	// reader what a change reaches.
	const consumers = $derived.by(() => {
		const byName: Record<string, string[]> = {};
		const note = (expr: Expression | undefined, app: string) => {
			for (const part of expr?.parts ?? []) {
				if (part.kind !== 'project_variable' || !part.name) continue;
				const apps = (byName[part.name] ??= []);
				if (!apps.includes(app)) apps.push(app);
			}
		};
		for (const [key, app] of Object.entries(data.definition?.applications ?? {})) {
			for (const expr of Object.values(app.environment ?? {})) note(expr, key);
			for (const route of Object.values(app.routes ?? {})) note(route.domain, key);
		}
		return byName;
	});
	const projectName = $derived(data.project.name);
	const entryByName = $derived(new Map(data.values.map((v) => [v.name, v])));
	const declaredNames = $derived(new Set(declared.map((d) => d.name)));
	const orphanedEntries = $derived(data.values.filter((v) => !declaredNames.has(v.name)));
	const dirtyCount = $derived(Object.keys(dirty).length);

	// Values are configuration: maintain on the environment edits them,
	// read sees names and versions only, none sees nothing.
	const envLocked = $derived(data.env?.access === 'none');
	const mayEdit = $derived(roleAtLeast(data.env?.access, 'maintain'));
	const editTitle = $derived(
		mayEdit ? undefined : requiredTitle('maintain', 'environment', data.env?.name ?? '')
	);

	async function save() {
		if (!data.env || dirtyCount === 0) return;
		saving = true;
		errorMessage = '';
		try {
			const res = await api.put<StageValuesResult>(`/v1/environments/${data.env.id}/values`, {
				values: { ...dirty },
				apply: true
			});
			toast.success('Values saved', {
				description: 'Stored for the environment; hit Redeploy to roll them out.'
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
		<Button
			variant="primary"
			busy={saving}
			disabled={!mayEdit || dirtyCount === 0}
			title={editTitle}
			onclick={save}
		>
			Save {dirtyCount > 0 ? dirtyCount : ''} change{dirtyCount === 1 ? '' : 's'}
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
{:else if envLocked}
	<EmptyState
		icon={Lock}
		title="This environment is locked for you"
		description="Your role on {data.env.name} is none; ask a project admin for access."
	/>
{:else if declared.length === 0 && orphanedEntries.length === 0}
	<EmptyState
		icon={KeyRound}
		title="No variables referenced"
		description="reference values with $&lbrace;VAR&rbrace; in skali.yaml; they become editable here"
	/>
{:else}
	<div class="flex flex-col gap-3.5 pb-6">
		{#if errorMessage}
			<div
				class="border-status-danger/40 bg-status-danger/10 text-status-danger rounded-[11px] border px-4 py-3 text-base"
			>
				{errorMessage}
			</div>
		{/if}

		<Card class="p-5 pb-2.5">
			<div class="mb-3 flex items-baseline gap-2.5">
				<h3 class="text-text-primary text-xl font-semibold">Variables</h3>
				<Pill text={data.env.name} />
				<span class="text-text-muted text-md">
					write-only · saved values roll out with Redeploy or the next deployment
					{#if !mayEdit}
						· {editTitle} to change
					{/if}
				</span>
			</div>
			<div class="flex flex-col">
				{#each declared as variable (variable.name)}
					{@const entry = entryByName.get(variable.name)}
					{@const pending = dirty[variable.name]}
					{@const missing = !entry && variable.required && !variable.hasDefault}
					{@const users = (consumers[variable.name] ?? []).toSorted()}
					<!-- The field tells the value's story: what is stored shows as a
					     masked placeholder with its version, a missing required value
					     as an amber field, a pending edit as an accent one. -->
					<div
						class="border-border-subtle grid grid-cols-[minmax(0,1.1fr)_minmax(0,2fr)_auto] items-center gap-3 border-b py-2.5 last:border-0"
					>
						<div class="flex min-w-0 flex-col gap-1" title={variable.name}>
							<span class="font-mono text-text-primary truncate text-md">{variable.name}</span>
							<div class="flex flex-wrap items-center gap-1">
								{#each users as app (app)}
									<!-- eslint-disable svelte/no-navigation-without-resolve -- path built with resolve(), env appended by $lib/urls -->
									<a
										href={withEnv(
											resolve('/(app)/projects/[project]/services/[service]', {
												project: projectName,
												service: app
											}),
											data.env?.name
										)}
										class="bg-service-app/12 text-service-app rounded-[6px] px-1.5 py-0.5 font-mono text-2xs hover:underline"
										title="read by {app}"
									>
										{app}
									</a>
									<!-- eslint-enable svelte/no-navigation-without-resolve -->
								{:else}
									<span class="font-mono text-text-ghost text-2xs">used at build time only</span>
								{/each}
							</div>
						</div>
						<SecretInput
							label={variable.name}
							disabled={!mayEdit}
							tone={pending !== undefined ? 'pending' : missing ? 'warning' : 'default'}
							placeholder={pending === ''
								? 'will be set to an empty value'
								: entry
									? '••••••••••••'
									: missing
										? 'required · not set'
										: variable.hasDefault
											? `default "${variable.default ?? ''}"`
											: 'not set'}
							value={pending ?? ''}
							oninput={(e) => {
								const next = (e.currentTarget as HTMLInputElement).value;
								if (next === '') delete dirty[variable.name];
								else dirty[variable.name] = next;
							}}
							onclear={pending !== undefined ? () => delete dirty[variable.name] : undefined}
						>
							{#snippet trailing()}
								{#if pending !== undefined}
									<span class="text-accent-light"
										>{pending === '' ? 'empty · unsaved' : 'unsaved'}</span
									>
								{:else if entry}
									<span title="stored version">v{entry.version}</span>
								{/if}
							{/snippet}
						</SecretInput>
						<Menu
							label="Actions on {variable.name}"
							align="end"
							triggerClass="flex size-7 cursor-pointer items-center justify-center rounded-[8px] text-text-tertiary transition-colors hover:bg-white/5 hover:text-text-primary disabled:cursor-default disabled:opacity-60"
						>
							{#snippet trigger()}
								<Ellipsis size={15} />
							{/snippet}
							<MenuItem
								disabled={!mayEdit || pending === ''}
								title={mayEdit ? 'Stage an empty string as the value.' : editTitle}
								onselect={() => (dirty[variable.name] = '')}
							>
								Set empty
							</MenuItem>
							<MenuItem
								danger
								disabled={!mayEdit || !entry}
								title={!mayEdit
									? editTitle
									: entry
										? 'Remove the stored value now.'
										: 'Nothing is stored.'}
								onselect={() =>
									unsetValue(variable.name, variable.required && !variable.hasDefault)}
							>
								Unset
							</MenuItem>
						</Menu>
					</div>
				{/each}
			</div>
		</Card>

		{#if orphanedEntries.length > 0}
			<Card class="p-5">
				<div class="mb-3.5 flex items-baseline gap-2.5">
					<h3 class="text-text-primary text-xl font-semibold">No longer referenced</h3>
					<span class="text-text-muted text-md">
						stored but not referenced by the current draft; ignored by deployments
					</span>
				</div>
				<div class="flex flex-col">
					{#each orphanedEntries as entry (entry.name)}
						<div
							class="border-border-subtle flex items-center gap-3 border-b py-2.75 last:border-0"
						>
							<span class="font-mono text-text-primary text-md">{entry.name}</span>
							<span class="font-mono text-text-faint text-xs">v{entry.version}</span>
							<div class="ml-auto">
								<Button
									size="sm"
									variant="ghost"
									disabled={!mayEdit}
									title={editTitle}
									onclick={() => deleteOrphan(entry.name)}
								>
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
