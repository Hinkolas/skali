<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import { api, ApiError } from '$lib/api/client';
	import { formatDateTime, formatDuration, relativeTime } from '$lib/format';
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
		OPERATION_PHASE_LABEL,
		operationSettled,
		type UpdateChannel,
		type UpdateStatus,
		type UpdateStepPhase
	} from '$lib/types/updates';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	// Seeded from the server load, then overwritten by polling: every 3s
	// while an update runs (the control plane itself restarts mid-way, so a
	// failed fetch keeps the last document and says so), once a minute
	// otherwise. A navigation reseeds it.
	let status = $derived<UpdateStatus>(data.status);
	let disconnected = $state(false);
	let busy = $state<'scan' | 'apply' | 'resume' | 'settings' | null>(null);

	const operation = $derived(status.operation);
	const running = $derived(operation != null && !operationSettled(operation));

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

	const progress = $derived.by(() => {
		if (!operation) return { done: 0, total: 0, pct: 0 };
		// The bundle move is the last step after every node; count it so
		// the bar does not sit at 100% while the platform still rolls.
		const total = operation.steps.length + 1;
		let done = operation.steps.filter((s) => s.phase === 'complete').length;
		if (operation.phase === 'complete') done = total;
		return { done, total, pct: total > 0 ? Math.round((done / total) * 100) : 0 };
	});

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

	const updateTitle = $derived.by(() => {
		if (!status.managed) return status.reason ?? 'not manageable from the console';
		if (!status.manageable) return status.reason ?? 'the cluster cannot take an update right now';
		return undefined;
	});
	const canUpdate = $derived(
		status.managed && status.manageable && status.update_available && status.latest != null
	);

	function describeError(err: unknown, fallback: string) {
		return err instanceof ApiError ? err.message : fallback;
	}

	async function scan() {
		busy = 'scan';
		try {
			status = await toast.promise(api.post<UpdateStatus>('/v1/system/updates/scan'), {
				loading: 'Checking for updates',
				success: (s) =>
					s.update_available && s.latest
						? `${s.latest.version} is available`
						: 'You are up to date',
				error: (err) => describeError(err, 'Could not check for updates')
			});
			await invalidateAll();
		} catch {
			// The toast already explained it.
		} finally {
			busy = null;
		}
	}

	async function apply() {
		const target = status.latest?.version;
		if (!target) return;
		const confirmed = await dialog.confirm({
			variant: 'danger',
			title: `Update to ${target}?`,
			description:
				'Every node moves to the new release one at a time, servers first, and the control plane restarts. Running applications keep serving; the console disconnects briefly.',
			confirmLabel: 'Update now'
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
		<span>skalid {status.installed.version}</span>
		{#if disconnected}
			<span class="text-status-warning flex items-center gap-1.5">
				<LoaderCircle class="size-3.5 animate-spin" />
				control plane restarting
			</span>
		{/if}
	{/snippet}
	{#snippet actions()}
		<Button variant="secondary" onclick={scan} busy={busy === 'scan'} disabled={busy != null}>
			Check for updates
		</Button>
	{/snippet}
</PageHeader>

<div class="flex max-w-3xl flex-col gap-3.5 pb-6">
	<!-- The update itself: what runs, what is available, and the button. -->
	<Card class="p-5">
		{#if running && operation}
			<div class="flex items-start gap-3">
				<div class="flex min-w-0 flex-1 flex-col gap-1">
					<h3 class="text-text-primary text-xl font-semibold">
						Updating to {operation.target_version ?? status.latest?.version ?? ''}
					</h3>
					<span class="text-text-muted text-md">
						{OPERATION_PHASE_LABEL[operation.phase] ?? operation.phase}
						· started {relativeTime(operation.started_at)}
						· {formatDuration(operation.started_at, null)}
					</span>
				</div>
				<span class="font-mono text-text-muted text-sm">{progress.done}/{progress.total}</span>
			</div>
			<div class="mt-4">
				<ProgressBar pct={progress.pct} />
			</div>
		{:else if operation?.phase === 'failed'}
			<div class="flex items-start gap-3">
				<div class="flex min-w-0 flex-1 flex-col gap-1">
					<h3 class="text-status-danger text-xl font-semibold">
						Update to {operation.target_version ?? ''} failed
					</h3>
					<span class="text-text-muted text-md">
						{operation.error ?? 'a step failed'} · {relativeTime(operation.updated_at)}
					</span>
				</div>
				<Button variant="primary" onclick={resume} busy={busy === 'resume'} disabled={busy != null}>
					Retry
				</Button>
			</div>
		{:else if status.update_available && status.latest}
			<div class="flex items-start gap-3">
				<div class="flex min-w-0 flex-1 flex-col gap-1">
					<h3 class="text-text-primary text-xl font-semibold">
						{status.latest.version}
						{#if status.latest.prerelease}
							<span class="text-text-muted ml-1 text-md font-normal">prerelease</span>
						{/if}
					</h3>
					<span class="text-text-muted text-md">
						published {formatDateTime(status.latest.published_at)}
						{#if status.latest.k3s}
							· includes k3s {status.latest.k3s}
						{/if}
					</span>
					{#if status.latest.url}
						<!-- eslint-disable svelte/no-navigation-without-resolve -- external release page -->
						<a
							href={status.latest.url}
							target="_blank"
							rel="noreferrer"
							class="text-accent-nav mt-1 flex items-center gap-1 text-md hover:underline"
						>
							Release notes
							<ExternalLink size={13} />
						</a>
						<!-- eslint-enable svelte/no-navigation-without-resolve -->
					{/if}
				</div>
				<Button
					variant="primary"
					onclick={apply}
					busy={busy === 'apply'}
					disabled={busy != null || !canUpdate}
					title={updateTitle}
				>
					Update now
				</Button>
			</div>
			{#if updateTitle}
				<p class="text-text-muted mt-4 text-md">
					{updateTitle}
					{#if !status.managed}
						<span class="font-mono text-text-secondary ml-1">skali cluster upgrade</span>
					{/if}
				</p>
			{/if}
		{:else}
			<div class="flex flex-col gap-1">
				<h3 class="text-text-primary text-xl font-semibold">
					{#if status.last_error}
						Could not check for updates
					{:else if status.last_checked_at}
						You are up to date
					{:else}
						Not checked yet
					{/if}
				</h3>
				<span class="text-text-muted text-md">
					{#if status.last_error}
						<span class="text-status-warning">{status.last_error}</span>
						{#if status.last_checked_at}
							· tried {relativeTime(status.last_checked_at)}
						{/if}
					{:else if status.last_checked_at}
						last checked {relativeTime(status.last_checked_at)}
						{#if status.latest}
							· latest release {status.latest.version}
						{/if}
					{:else}
						the daily scan has not run; check now to ask the release feed
					{/if}
				</span>
			</div>
			{#if operation?.phase === 'complete'}
				<p class="text-text-faint mt-3 text-md">
					Last update to {operation.target_version ?? ''} completed
					{relativeTime(operation.completed_at ?? operation.updated_at)}.
				</p>
			{/if}
		{/if}

		<div class="mt-5">
			<KeyValueRow k="Installed" v={status.installed.version} />
			{#if status.installed.platform_version && status.installed.platform_version !== status.installed.version}
				<KeyValueRow k="Cluster" v={status.installed.platform_version} />
			{/if}
			<KeyValueRow k="Channel" v={status.channel} />
			<KeyValueRow
				k="Last check"
				v={status.last_checked_at ? formatDateTime(status.last_checked_at) : 'never'}
			/>
		</div>
	</Card>

	<!-- Per-node progress while an update runs, or after it failed. -->
	{#if operation && (running || operation.phase === 'failed') && operation.steps.length > 0}
		<Card class="p-5">
			<h3 class="text-text-primary text-xl font-semibold">Nodes</h3>
			<div class="mt-3">
				{#each operation.steps as step (step.node)}
					<div class="border-border-subtle flex items-center gap-3 border-b py-2.5 last:border-0">
						<span class="size-[8px] flex-none rounded-full {stepDot[step.phase]}"></span>
						<span class="font-mono text-text-primary text-md">{step.node}</span>
						<span class="text-text-faint text-md">{step.action}</span>
						<span class="flex-1"></span>
						<span
							class="text-md {step.phase === 'failed' ? 'text-status-danger' : 'text-text-muted'}"
							title={step.error}
						>
							{stepLabel[step.phase]}
						</span>
					</div>
				{/each}
				<div class="flex items-center gap-3 py-2.5">
					<span
						class="size-[8px] flex-none rounded-full {operation.phase === 'complete'
							? 'bg-status-success'
							: operation.phase === 'reconciling-platform' || operation.phase === 'verifying'
								? 'bg-status-warning animate-pulse'
								: 'bg-white/15'}"
					></span>
					<span class="text-text-primary text-md">platform bundle</span>
					<span class="text-text-faint text-md">skalid, console, registry</span>
				</div>
			</div>
		</Card>
	{/if}

	<!-- Settings: the daily scan's channel and what it may do on its own. -->
	<Card class="p-5">
		<h3 class="text-text-primary text-xl font-semibold">Automatic updates</h3>
		<div class="mt-4 flex flex-col gap-4">
			<Toggle
				checked={status.auto_update}
				label="Install updates automatically"
				description="The daily check starts an update as soon as a newer release is available and nothing is deploying."
				disabled={busy != null}
				onchange={(checked) => saveSettings(status.channel, checked)}
			/>
			<div class="flex flex-col gap-1.5">
				<span class="text-text-tertiary text-base font-medium">Channel</span>
				<div class="flex gap-2">
					{#each ['stable', 'beta'] as const as channel (channel)}
						<button
							type="button"
							disabled={busy != null}
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
				<span class="text-text-muted text-base">
					{status.channel === 'beta'
						? 'Beta also follows prereleases (alpha, beta, rc). Expect rough edges.'
						: 'Stable follows tagged releases only.'}
				</span>
			</div>
		</div>
	</Card>

	<!-- What every node runs, from the coordinator. -->
	{#if status.managed}
		<Card class="p-5">
			<h3 class="text-text-primary text-xl font-semibold">Cluster</h3>
			<div class="mt-3">
				{#each status.nodes as node (node.name)}
					<div class="border-border-subtle flex items-center gap-3 border-b py-2.5 last:border-0">
						<span class="font-mono text-text-primary text-md">{node.name}</span>
						<Pill text={node.role} />
						<span class="flex-1"></span>
						<span class="font-mono text-text-muted text-sm" title="k3s">
							{node.k3s_version ?? 'k3s unknown'}
						</span>
						<span class="font-mono text-text-muted text-sm" title="skali-hostd">
							{node.agent_version ?? 'hostd unknown'}
						</span>
						<Pill text={node.phase} tone={nodePhaseTone[node.phase] ?? 'neutral'} />
					</div>
				{/each}
			</div>
		</Card>
	{/if}
</div>
