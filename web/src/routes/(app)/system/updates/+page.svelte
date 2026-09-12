<script lang="ts">
	import { tick } from 'svelte';
	import { invalidateAll } from '$app/navigation';
	import CloudOff from '@lucide/svelte/icons/cloud-off';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, ApiError } from '$lib/api/client';
	import { formatDateTime, relativeTime } from '$lib/format';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import Toggle from '$lib/components/ui/Toggle.svelte';
	import {
		FEED_ERROR_HINT,
		FEED_ERROR_TITLE,
		type UpdateChannel,
		type UpdateStatus,
		updatePresentation,
		updateSummary,
		type UpdateStepPhase
	} from '$lib/types/updates';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// Seeded from the route load, then overwritten by polling: every 3s
	// while an update runs (the control plane itself restarts mid-way, so a
	// failed fetch keeps the last document and says so), once a minute
	// otherwise. A navigation reseeds it.
	let status = $derived<UpdateStatus>(data.status);
	let disconnected = $state(false);
	let busy = $state<'scan' | 'apply' | 'resume' | 'settings' | null>(null);

	const operation = $derived(status.operation);
	const summary = $derived(updateSummary(status));
	const running = $derived(summary?.state === 'updating');
	const presentation = $derived(updatePresentation(status));
	let detailsOpen = $state(false);
	let detailsElement: HTMLDetailsElement;
	async function showDetails(failedStep = false) {
		detailsOpen = true;
		await tick();
		const id = failedStep
			? operation?.steps.find((step) => step.phase === 'failed')?.node_id
			: undefined;
		const destination = id ? document.getElementById(`update-node-${id}`) : detailsElement;
		destination?.scrollIntoView({ block: 'start' });
		if (id) destination?.focus();
		else detailsElement?.querySelector('summary')?.focus();
	}

	$effect(() => {
		const interval = running ? 3_000 : 60_000;
		let cancelled = false;
		const refresh = async () => {
			try {
				const fresh = await api.get<UpdateStatus>('/v1/system/updates');
				if (cancelled) return;
				status = fresh;
				disconnected = false;
			} catch {
				// The daemon is rolling or unreachable; keep the last document.
				if (!cancelled) disconnected = true;
			}
		};
		const timer = setInterval(refresh, interval);
		return () => {
			cancelled = true;
			clearInterval(timer);
		};
	});

	const progress = $derived(summary.progress);

	const stepDot: Record<UpdateStepPhase, string> = {
		pending: 'bg-white/15',
		running: 'bg-status-warning animate-pulse',
		complete: 'bg-status-success',
		failed: 'bg-status-danger'
	};
	const stepLabel: Record<UpdateStepPhase, string> = {
		pending: 'waiting',
		running: 'upgrading',
		complete: 'done',
		failed: 'failed'
	};

	// The last scan failed: worded by kind, shown above every branch so it
	// is not hidden behind a release an earlier scan found.
	const feedFailure = $derived.by(() => {
		if (!status.last_error) return null;
		const kind = status.last_error_kind;
		return {
			offline: kind === 'offline' || kind === 'unavailable',
			title: kind ? FEED_ERROR_TITLE[kind] : 'Could not check for updates',
			hint: kind ? FEED_ERROR_HINT[kind] : 'The last check did not complete.',
			detail: status.last_error
		};
	});

	const updateTitle = $derived.by(() => {
		if (!status.managed) return status.reason ?? 'not manageable from the console';
		if (!status.manageable) return status.reason ?? 'the cluster cannot take an update right now';
		return undefined;
	});
	const canUpdate = $derived(
		status.managed && (status.manageable || summary.action === 'retry') && summary.action !== ''
	);

	function describeError(err: unknown, fallback: string) {
		return err instanceof ApiError ? err.message : fallback;
	}

	// A scan that reaches the daemon but not the feed is not a success: the
	// toast takes the failure's tone and title, the notice above carries
	// the detail.
	async function scan() {
		busy = 'scan';
		const id = toast.loading('Checking for updates');
		try {
			status = await api.post<UpdateStatus>('/v1/system/updates/scan');
			if (status.last_error) {
				toast.update(id, {
					variant: 'warning',
					title: status.last_error_kind
						? FEED_ERROR_TITLE[status.last_error_kind]
						: 'Could not check for updates'
				});
			} else {
				toast.update(id, {
					variant: 'success',
					title: updatePresentation(status).title
				});
			}
			await invalidateAll();
		} catch (err) {
			toast.update(id, {
				variant: 'error',
				title: describeError(err, 'Could not check for updates')
			});
		} finally {
			busy = null;
		}
	}

	async function apply() {
		const target = summary.target_version;
		if (!target) return;
		const confirmed = await dialog.confirm({
			variant: 'danger',
			title: `${summary.action === 'finish' ? 'Finish update to' : 'Update to'} ${target}?`,
			description:
				'Every node moves to the new release one at a time, servers first, and the control plane restarts. Running applications keep serving; the console disconnects briefly.',
			confirmLabel: presentation.action
		});
		if (!confirmed) return;
		busy = 'apply';
		try {
			status = await api.post<UpdateStatus>('/v1/system/updates/apply', { version: target });
			toast.success(`Updating to ${target}`);
		} catch (err) {
			toast.error(describeError(err, 'Could not start the update'));
		} finally {
			busy = null;
		}
	}

	async function resume() {
		busy = 'resume';
		try {
			status = await api.post<UpdateStatus>('/v1/system/updates/resume');
			toast.success('Update resumed');
		} catch (err) {
			toast.error(describeError(err, 'Could not resume the update'));
		} finally {
			busy = null;
		}
	}

	async function saveSettings(channel: UpdateChannel, autoUpdate: boolean) {
		busy = 'settings';
		try {
			status = await api.put<UpdateStatus>('/v1/system/updates/settings', {
				channel,
				auto_update: autoUpdate
			});
			await invalidateAll();
		} catch (err) {
			toast.error(describeError(err, 'Could not save the update settings'));
		} finally {
			busy = null;
		}
	}

	const nodePhaseTone: Record<string, 'success' | 'warning' | 'neutral'> = {
		active: 'success',
		failed: 'warning'
	};
</script>

<svelte:head>
	<title>Updates — skali</title>
</svelte:head>

<PageHeader title="Software update">
	{#snippet subtitle()}
		<span>One release for the platform and every node</span>
	{/snippet}
	{#snippet actions()}
		<Button variant="secondary" onclick={scan} busy={busy === 'scan'} disabled={busy != null}>
			Check for updates
		</Button>
	{/snippet}
</PageHeader>

<div class="grid grid-cols-1 gap-3.5 @4xl:grid-cols-2 pb-6">
	{#if disconnected}
		<p class="text-status-warning @4xl:col-span-2 flex items-center gap-2 text-md" role="status">
			<LoaderCircle class="size-4 animate-spin" />
			Reconnecting to the platform. An accepted update continues in the background.
		</p>
	{/if}
	{#if feedFailure}
		<div
			class="border-status-warning/25 bg-status-warning/10 @4xl:col-span-2 flex items-start gap-3 rounded-[13px] border p-4"
			role="status"
		>
			{#if feedFailure.offline}<CloudOff size={18} />{:else}<TriangleAlert size={18} />{/if}
			<div class="min-w-0">
				<p class="text-status-warning text-md font-medium">{feedFailure.title}</p>
				<p class="text-text-muted mt-1 text-md">{feedFailure.hint}</p>
				<button
					class="text-accent-nav mt-2 cursor-pointer text-md underline underline-offset-4"
					onclick={() => showDetails()}>View details</button
				>
			</div>
		</div>
	{/if}

	<Card class="p-5">
		<div class="flex flex-col items-start gap-4 sm:flex-row sm:justify-between">
			<div class="w-full min-w-0 sm:w-auto sm:flex-1">
				<h2 class="text-text-primary text-xl font-semibold">{presentation.title}</h2>
				{#if summary.detail}<p class="text-text-muted mt-1 max-w-prose text-md">
						{summary.detail}
					</p>{/if}
				{#if summary.state === 'incomplete'}
					<p class="text-text-muted mt-1 text-md">
						Finish updating to {summary.target_version}.
					</p>
				{/if}
				{#if summary.state === 'failed'}
					<button
						class="text-accent-nav mt-2 cursor-pointer text-md underline underline-offset-4"
						onclick={() => showDetails(true)}>View failed step</button
					>
				{/if}
			</div>
			{#if summary.action}
				<Button
					variant="primary"
					onclick={summary.action === 'retry' ? resume : apply}
					busy={busy === 'apply' || busy === 'resume'}
					disabled={busy != null || !canUpdate}
					title={summary.action === 'retry' ? 'Continue the remaining update steps' : updateTitle}
				>
					{presentation.action}
				</Button>
			{/if}
		</div>
		{#if running}
			<div
				class="mt-5"
				role="progressbar"
				aria-label={progress.phase}
				aria-valuemin={0}
				aria-valuemax={100}
				aria-valuenow={progress.percent}
			>
				<div class="text-text-muted mb-2 flex justify-between gap-4 text-md" aria-live="polite">
					<span>{progress.phase}</span><span>{progress.done}/{progress.total}</span>
				</div>
				<ProgressBar pct={progress.percent} />
			</div>
		{/if}
		<!-- The reason updating is unavailable often is the status detail
		     itself; say it once. -->
		{#if updateTitle && !running && summary.action !== 'retry' && updateTitle !== summary.detail}
			<p class="text-text-muted mt-3 text-md">{updateTitle}</p>
		{/if}
		<div class="mt-5">
			<KeyValueRow k="Skali version" v={summary.converged_version ?? 'not verified'} />
			{#if running || summary.action === 'finish' || summary.action === 'retry'}
				<KeyValueRow
					k={running ? 'Updating to' : 'Target version'}
					v={summary.target_version ?? 'unknown'}
				/>
			{/if}
			<KeyValueRow
				k="Last check"
				v={status.last_checked_at ? relativeTime(status.last_checked_at) : 'never'}
			/>
		</div>
		{#if status.latest && summary.action === 'update'}
			<p class="text-text-muted mt-3 text-md">
				Released {formatDateTime(status.latest.published_at)}
			</p>
			{#if status.latest.url}
				<!-- eslint-disable svelte/no-navigation-without-resolve -- external release page -->
				<a
					href={status.latest.url}
					target="_blank"
					rel="noreferrer"
					class="text-accent-nav mt-2 inline-flex items-center gap-1 text-md hover:underline"
				>
					Release notes <ExternalLink size={13} />
				</a>
				<!-- eslint-enable svelte/no-navigation-without-resolve -->
			{/if}
		{/if}
		{#if status.last_successful}
			<p class="text-text-faint mt-3 text-md">
				Last successful update: {status.last_successful.target_version}, {relativeTime(
					status.last_successful.completed_at ?? status.last_successful.updated_at
				)}.
			</p>
		{/if}
	</Card>

	<Card class="p-5">
		<h2 class="text-text-primary text-xl font-semibold">Automatic updates</h2>
		<div class="mt-4 flex flex-col gap-4">
			<Toggle
				checked={status.auto_update}
				label="Install updates automatically"
				description="Install new releases for the whole cluster after the daily check. Incomplete updates require your attention."
				disabled={busy != null}
				onchange={(checked) => saveSettings(status.channel, checked)}
			/>
			<fieldset class="flex flex-col gap-1.5">
				<legend class="text-text-tertiary mb-1.5 text-base font-medium">Channel</legend>
				<div class="flex gap-2">
					{#each ['stable', 'beta'] as const as channel (channel)}
						<button
							type="button"
							disabled={busy != null}
							aria-pressed={status.channel === channel}
							onclick={() =>
								channel !== status.channel && saveSettings(channel, status.auto_update)}
							class="flex-1 cursor-pointer rounded-[11px] border px-3 py-2.5 text-lg font-medium transition-colors disabled:cursor-default {status.channel ===
							channel
								? 'border-accent/50 bg-accent/10 text-accent-nav'
								: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
						>
							{channel}
						</button>
					{/each}
				</div>
				<p class="text-text-muted text-base">
					{status.channel === 'beta'
						? 'Beta includes alpha, beta, and release candidates.'
						: 'Stable includes stable releases only. Choose beta for alpha releases.'}
				</p>
			</fieldset>
		</div>
	</Card>

	<Card class="@4xl:col-span-2 p-5">
		<details bind:this={detailsElement} bind:open={detailsOpen}>
			<summary class="text-text-primary cursor-pointer text-lg font-medium">Update details</summary>
			<div class="mt-4">
				<h3 class="text-text-primary mb-2 text-md font-medium">Platform</h3>
				<KeyValueRow k="skalid and console" v={status.installed.version} />
				<p class="text-text-muted mt-2 text-base">
					The platform runs inside Kubernetes. Host agents run on every node; coordinators run on
					controllers.
				</p>
				{#if status.last_error}<p class="text-status-warning mt-3 text-sm break-words">
						{status.last_error}
					</p>{/if}
				{#if operation?.error}<p class="text-status-warning mt-3 text-sm break-words">
						{operation.error}
					</p>{/if}
				<h3 class="text-text-primary mt-5 text-md font-medium">Nodes</h3>
				{#each status.nodes as node (node.id)}
					{@const step = operation?.steps.find((step) => step.node_id === node.id)}
					<div
						id={`update-node-${node.id}`}
						tabindex="-1"
						class="border-border-subtle border-b py-4 last:border-0"
					>
						<div class="flex flex-wrap items-center gap-2">
							<span class="font-mono text-text-primary text-md break-all">{node.name}</span>
							<Pill text={node.role === 'server' ? 'controller' : 'worker'} />
							<Pill text={node.phase} tone={nodePhaseTone[node.phase] ?? 'neutral'} />
						</div>
						<dl
							class="text-text-muted mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm"
						>
							<dt>Host agent</dt>
							<dd class="font-mono break-all">{node.agent_version ?? 'not reported'}</dd>
							{#if node.role === 'server'}<dt>Coordinator</dt>
								<dd class="font-mono break-all">
									{node.coordinator_version ?? 'not reported'}
								</dd>{/if}
							<dt>Kubernetes</dt>
							<dd class="font-mono break-all">{node.k3s_version ?? 'not reported'}</dd>
							<dt>Agent report</dt>
							<dd>{node.last_seen ? relativeTime(node.last_seen) : 'never'}</dd>
							{#if node.role === 'server'}
								<dt>Coordinator report</dt>
								<dd>
									{node.coordinator_last_seen ? relativeTime(node.coordinator_last_seen) : 'never'}
								</dd>
							{/if}
						</dl>
						{#if step && operation?.phase !== 'complete'}
							<p class="text-text-muted mt-2 flex items-center gap-2 text-md">
								<span class="size-2 rounded-full {stepDot[step.phase]}"></span>{stepLabel[
									step.phase
								]}
							</p>
							{#if step.error}<p class="text-status-warning mt-1 text-sm break-words">
									{step.error}
								</p>{/if}
						{/if}
					</div>
				{/each}
			</div>
		</details>
	</Card>
</div>
