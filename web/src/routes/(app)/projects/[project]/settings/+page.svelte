<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { page } from '$app/state';
	import Ellipsis from '@lucide/svelte/icons/ellipsis';
	import Lock from '@lucide/svelte/icons/lock';
	import Plus from '@lucide/svelte/icons/plus';
	import { api, ApiError } from '$lib/api/client';
	import { isInstanceAdmin, requiredTitle, roleAtLeast } from '$lib/access';
	import { formatDateTime, relativeTime } from '$lib/format';
	import { HEALTH_META } from '$lib/service-types';
	import { withEnv } from '$lib/urls';
	import type { RevisionSummary } from '$lib/types/revisions';
	import type { EnvironmentStatus } from '$lib/types/status';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { modal } from '$lib/stores/modal.svelte';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import Field from '$lib/components/ui/Field.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import Menu from '$lib/components/ui/Menu.svelte';
	import MenuItem from '$lib/components/ui/MenuItem.svelte';
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
	import EnvironmentSettingsModal, {
		modalOptions as environmentSettingsModalOptions
	} from '$lib/components/access/EnvironmentSettingsModal.svelte';
	import type { AuthUser } from '$lib/types/auth';
	import type { Environment } from '$lib/types/project';
	import type { Target } from '$lib/types/revisions';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// Access: the project role gates project settings and environment
	// creation; each environment's effective role gates its own controls.
	const user = $derived(page.data.user as AuthUser | null);
	const instanceAdmin = $derived(isInstanceAdmin(user));
	const projectAdmin = $derived(roleAtLeast(data.project.access.role, 'admin'));
	const projectMaintain = $derived(roleAtLeast(data.project.access.role, 'maintain'));
	const projectAdminTitle = $derived(requiredTitle('admin', 'project', data.project.name));
	const envLocked = $derived(data.env?.access === 'none');

	function openEnvironmentSettings(environment: Environment) {
		if (!environment.settings) return;
		modal.open(
			EnvironmentSettingsModal,
			{
				environment,
				environments: data.environments,
				canEdit: roleAtLeast(environment.access, 'admin'),
				instanceAdmin
			},
			environmentSettingsModalOptions
		);
	}

	function environmentPills(environment: Environment) {
		const pills: { text: string; tone: 'success' | 'warning' | 'neutral'; title: string }[] = [];
		if (environment.access === 'none') {
			pills.push({
				text: 'locked',
				tone: 'neutral',
				title: 'Locked for you: listed by name only.'
			});
			return pills;
		}
		if (environment.settings?.deploy_policy === 'promote-only') {
			const sources = environment.settings.promote_from;
			pills.push({
				text: 'protected',
				tone: 'warning',
				title: `promote-only: direct deploys are refused; promote from ${
					sources.length > 0 ? sources.join(' or ') : 'any environment'
				}`
			});
		}
		if (environment.settings?.priority === 'high') {
			pills.push({
				text: 'high',
				tone: 'warning',
				title: 'high priority: keeps running when resources are tight'
			});
		}
		return pills;
	}

	// One line of live facts per environment: aggregate health, state, and
	// the newest deploy. Health follows the project
	// overview's rule: all healthy is green, any unhealthy is red, anything
	// else in between is amber; an unobserved environment stays grey.
	function healthDot(status: EnvironmentStatus | null): string {
		if (!status || status.services.length === 0) return HEALTH_META.unknown.dot;
		if (status.services.every((s) => s.health === 'healthy')) return HEALTH_META.healthy.dot;
		if (status.services.some((s) => s.health === 'unhealthy')) return HEALTH_META.unhealthy.dot;
		if (status.services.every((s) => s.health === 'unknown')) return HEALTH_META.unknown.dot;
		return HEALTH_META.degraded.dot;
	}
	function environmentFacts(environment: Environment): string[] {
		const insight = data.insights[environment.id];
		if (!insight) return [];
		const facts: string[] = [];
		const status = insight.status;
		if (status) {
			facts.push(status.state);
			if (status.services.length > 0) {
				const healthy = status.services.filter((s) => s.health === 'healthy').length;
				facts.push(`${healthy}/${status.services.length} healthy`);
			}
		}
		const deploy = insight.lastDeploy;
		if (deploy) {
			const who = deploy.actor.split('@')[0];
			const when = relativeTime(deploy.finished_at ?? deploy.started_at ?? deploy.created_at);
			const verb = deploy.kind === 'rollback' ? 'rolled back' : 'deployed';
			if (deploy.status === 'running' || deploy.status === 'pending') {
				facts.push(`${deploy.kind === 'rollback' ? 'rolling back' : 'deploying'} · ${who}`);
			} else if (deploy.status === 'succeeded') {
				facts.push(`${verb} ${when} by ${who}`);
			} else {
				facts.push(`${deploy.kind} ${deploy.status} ${when} · ${who}`);
			}
		} else if (status) {
			facts.push('never deployed');
		}
		return facts;
	}
	// The row's title carries the slow-moving facts the line leaves out.
	function environmentTitle(environment: Environment): string {
		const parts = [`your role: ${environment.access}`];
		if (environment.created_at) parts.push(`created ${formatDateTime(environment.created_at)}`);
		return parts.join(' · ');
	}

	// What a revision changed against the one before it (the list is newest
	// first): the manifest, the values, or both. The oldest is the initial one.
	function revisionChange(revision: RevisionSummary, index: number): string {
		const previous = data.revisions[index + 1];
		if (!previous) return 'initial';
		const manifest = previous.definition_hash !== revision.definition_hash;
		const values = previous.values_hash !== revision.values_hash;
		if (manifest && values) return 'manifest + values';
		if (manifest) return 'manifest';
		if (values) return 'values';
		return 'redeploy';
	}

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
			// Purging is one-way; teardown is resurrectable and stays one-click.
			typeToConfirm: purge ? envName : undefined,
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

<div class="grid grid-cols-2 gap-3.5 pb-6">
	<Card class="p-5">
		<h3 class="text-text-primary mb-3.5 text-xl font-semibold">General</h3>
		<Field
			label="Display name"
			description="Shown in lists and headers; empty falls back to the name."
		>
			<div class="flex gap-2">
				<TextInput
					bind:value={displayName}
					placeholder={data.project.name}
					disabled={!projectAdmin}
				/>
				<Button
					busy={savingName}
					disabled={!projectAdmin || displayName === data.project.display_name}
					title={projectAdmin ? undefined : projectAdminTitle}
					onclick={saveDisplayName}
				>
					Save
				</Button>
			</div>
		</Field>
		<!-- Everything else about a project is decided by the manifest and
		     the CLI; stated as facts rather than dressed as disabled inputs. -->
		<div class="mt-4 flex flex-col">
			<KeyValueRow k="Name" v={data.project.name} labelWidth="w-36" />
			<KeyValueRow
				k="Source"
				v={data.project.source_mode === 'file' ? 'file · skali.yaml in the repository' : 'managed'}
				labelWidth="w-36"
			/>
			<KeyValueRow
				k="Manifest"
				v={data.draft
					? `draft v${data.draft.version} · ${data.draft.hash.slice(0, 10)} · ${data.services.length} service${data.services.length === 1 ? '' : 's'}`
					: 'no draft yet · the first deploy submits one'}
				labelWidth="w-36"
			/>
			<KeyValueRow k="Created" v={formatDateTime(data.project.created_at)} labelWidth="w-36" />
		</div>
	</Card>

	<Card class="p-5 pb-2.5">
		<div class="mb-3 flex items-center">
			<h3 class="text-text-primary text-xl font-semibold">Environments</h3>
			<div class="ml-auto">
				<Button
					size="sm"
					disabled={!projectMaintain}
					title={projectMaintain
						? undefined
						: requiredTitle('maintain', 'project', data.project.name)}
					onclick={() =>
						modal.open(NewEnvironmentModal, { project: data.project }, newEnvironmentModalOptions)}
				>
					<Plus size={14} /> New environment
				</Button>
			</div>
		</div>
		<div class="flex flex-col">
			{#each data.environments as environment (environment.id)}
				{@const locked = environment.access === 'none'}
				{@const envAdmin = roleAtLeast(environment.access, 'admin')}
				{@const envAdminTitle = requiredTitle('admin', 'environment', environment.name)}
				{@const facts = environmentFacts(environment)}
				<div
					class="border-border-subtle flex items-center gap-3 border-b py-2.5 last:border-0"
					title={environmentTitle(environment)}
				>
					<span
						class="size-[8px] flex-none rounded-full {locked
							? 'bg-text-ghost'
							: healthDot(data.insights[environment.id]?.status ?? null)}"
					></span>
					<div class="flex min-w-0 flex-1 flex-col gap-1">
						<div class="flex flex-wrap items-center gap-x-3 gap-y-1">
							{#if locked}
								<Lock size={13} class="text-text-ghost flex-none" />
								<span class="font-mono text-text-primary text-md">{environment.name}</span>
							{:else}
								<!-- eslint-disable svelte/no-navigation-without-resolve -- same page, env switched by $lib/urls -->
								<a
									href={withEnv(page.url.pathname, environment.name)}
									class="font-mono text-text-primary text-md hover:underline"
									title="switch the console to {environment.name}"
								>
									{environment.name}
								</a>
								<!-- eslint-enable svelte/no-navigation-without-resolve -->
							{/if}
							{#if environment.id === data.env?.id}
								<Pill text="current" tone="success" />
							{/if}
							{#each environmentPills(environment) as pill (pill.text)}
								<span title={pill.title}><Pill text={pill.text} tone={pill.tone} /></span>
							{/each}
						</div>
						{#if facts.length > 0}
							<div class="font-mono text-text-faint flex flex-wrap gap-x-2 text-xs">
								{#each facts as fact, i (i)}
									<span class="whitespace-nowrap">{fact}{i < facts.length - 1 ? ' ·' : ''}</span>
								{/each}
							</div>
						{/if}
					</div>
					<div class="ml-auto flex flex-none items-center gap-3">
						{#if !locked}
							<Button
								size="sm"
								variant="ghost"
								onclick={() => openEnvironmentSettings(environment)}
							>
								Settings
							</Button>
							<Menu
								label="Actions on {environment.name}"
								align="end"
								triggerClass="flex cursor-pointer items-center rounded-[8px] p-1.5 text-text-tertiary transition-colors hover:bg-white/5 hover:text-text-primary"
							>
								{#snippet trigger()}
									<Ellipsis size={15} />
								{/snippet}
								<MenuItem
									disabled={!envAdmin}
									title={envAdmin
										? 'Removes the running workloads but keeps values, revisions, and volumes.'
										: envAdminTitle}
									onselect={() => teardown(environment.id, environment.name, false)}
								>
									Tear down
								</MenuItem>
								<MenuItem
									danger
									disabled={!envAdmin}
									title={envAdmin
										? 'Destroys the namespace and deletes the environment. One-way.'
										: envAdminTitle}
									onselect={() => teardown(environment.id, environment.name, true)}
								>
									Purge environment
								</MenuItem>
							</Menu>
						{/if}
					</div>
				</div>
			{:else}
				<div class="font-mono text-text-faint py-2 text-md">no environments yet</div>
			{/each}
		</div>
	</Card>

	<Card class="col-span-2 p-5 pb-2.5">
		<div class="mb-3 flex items-center gap-2.5">
			<h3 class="text-text-primary text-xl font-semibold">Revisions</h3>
			{#if data.env}
				<Pill text={data.env.name} />
				<span class="text-text-muted text-md">what each deploy changed · newest first</span>
			{/if}
		</div>
		<div class="flex flex-col">
			{#if envLocked}
				<div class="font-mono text-text-faint flex items-center gap-1.5 py-2 text-md">
					<Lock size={12} /> this environment is locked for you
				</div>
			{/if}
			{#each data.revisions as revision, index (revision.id)}
				{@const isTarget = revision.id === data.target?.target_revision_id}
				{@const isActive = revision.id === data.target?.active_revision_id}
				{@const mayRollback = roleAtLeast(data.env?.access, 'deploy')}
				<div class="border-border-subtle flex items-center gap-3 border-b py-2.5 last:border-0">
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
						class="font-mono text-text-faint text-xs"
						title={formatDateTime(revision.created_at)}
					>
						{relativeTime(revision.created_at)}
					</span>
					<span
						class="font-mono text-text-muted text-xs"
						title="compared with the revision before it"
					>
						{revisionChange(revision, index)}
					</span>
					<div class="ml-auto">
						{#if !isTarget}
							<Button
								size="sm"
								variant="ghost"
								disabled={!mayRollback}
								title={mayRollback
									? undefined
									: requiredTitle('deploy', 'environment', data.env?.name ?? '')}
								onclick={() => rollback(revision.id)}
							>
								Roll back
							</Button>
						{/if}
					</div>
				</div>
			{:else}
				{#if !envLocked}
					<div class="font-mono text-text-faint py-2 text-md">
						no revisions yet · the first deploy creates one
					</div>
				{/if}
			{/each}
		</div>
	</Card>

	<Card class="border-status-danger/30 col-span-2 p-5">
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
					disabled={!projectAdmin}
					title={projectAdmin ? undefined : projectAdminTitle}
					onclick={() =>
						modal.open(DeleteProjectModal, { project: data.project }, deleteProjectModalOptions)}
				>
					Delete project
				</Button>
			</div>
		</div>
	</Card>
</div>
