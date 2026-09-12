<script lang="ts">
	import { untrack } from 'svelte';
	import { invalidateAll } from '$app/navigation';
	import ArrowDownToLine from '@lucide/svelte/icons/arrow-down-to-line';
	import ArrowRight from '@lucide/svelte/icons/arrow-right';
	import Check from '@lucide/svelte/icons/check';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleDashed from '@lucide/svelte/icons/circle-dashed';
	import CircleHelp from '@lucide/svelte/icons/circle-help';
	import CloudOff from '@lucide/svelte/icons/cloud-off';
	import ExternalLink from '@lucide/svelte/icons/external-link';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import type { Component } from 'svelte';
	import { api, ApiError } from '$lib/api/client';
	import { formatDateTime, relativeTime } from '$lib/format';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { sidepanel } from '$lib/stores/sidepanel.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import Pill from '$lib/components/ui/Pill.svelte';
	import ProgressBar from '$lib/components/ui/ProgressBar.svelte';
	import Toggle from '$lib/components/ui/Toggle.svelte';
	import UpdateDetailsPanel from '$lib/components/system/UpdateDetailsPanel.svelte';
	import {
		FEED_ERROR_HINT,
		FEED_ERROR_TITLE,
		commonVersion,
		tallyVersions,
		type UpdateChannel,
		type UpdateStatus,
		updatePresentation,
		updateSummary
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
	const kind = $derived(summary.state);
	const running = $derived(kind === 'updating');
	const presentation = $derived(updatePresentation(status));
	const progress = $derived(summary.progress);

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

	// The side panel shows the per-node picture. While it is open, every
	// fresh status document is pushed into it; the store keeps the same
	// component instance, so this is an in-place prop update.
	const PANEL_OPTIONS = { label: 'Update details', width: 'w-[440px]' };
	function openDetails(focusNode?: string) {
		sidepanel.open(UpdateDetailsPanel, { status, focusNode }, PANEL_OPTIONS);
	}
	$effect(() => {
		const fresh = status;
		untrack(() => {
			if (sidepanel.current?.component === UpdateDetailsPanel) {
				sidepanel.open(UpdateDetailsPanel, { status: fresh }, PANEL_OPTIONS);
			}
		});
	});

	// The last scan failed: worded by kind, shown above every branch so it
	// is not hidden behind a release an earlier scan found.
	const feedFailure = $derived.by(() => {
		if (!status.last_error) return null;
		const kind = status.last_error_kind;
		return {
			offline: kind === 'offline' || kind === 'unavailable',
			title: kind ? FEED_ERROR_TITLE[kind] : 'Could not check for updates',
			hint: kind ? FEED_ERROR_HINT[kind] : 'The last check did not complete.'
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

	// The hero: one glyph, one sentence, one action per state.
	type Tone = 'success' | 'accent' | 'warning' | 'danger' | 'neutral';
	const hero = $derived.by((): { icon: Component; tone: Tone; spin?: boolean; sub: string } => {
		const checked = status.last_checked_at
			? `checked ${relativeTime(status.last_checked_at)}`
			: 'never checked';
		switch (kind) {
			case 'current':
				return {
					icon: CircleCheck,
					tone: 'success',
					sub: `skali ${summary.converged_version ?? ''} · ${checked}`
				};
			case 'available':
				return {
					icon: ArrowDownToLine,
					tone: 'accent',
					sub: `you are on ${summary.converged_version ?? 'an older release'} · ${checked}`
				};
			case 'updating':
				return {
					icon: RefreshCw,
					tone: 'accent',
					spin: true,
					sub: operation
						? `started ${relativeTime(operation.started_at)}${operation.from_version ? ` from ${operation.from_version}` : ''}`
						: 'starting'
				};
			case 'failed':
				return {
					icon: TriangleAlert,
					tone: 'danger',
					sub: operation
						? `stopped ${relativeTime(operation.updated_at)} · started ${relativeTime(operation.started_at)}`
						: 'a step did not complete'
				};
			case 'incomplete':
				return {
					icon: TriangleAlert,
					tone: 'warning',
					sub: `components disagree on the release · finishing brings every node to ${summary.target_version}`
				};
			case 'not_checked':
				return {
					icon: CircleDashed,
					tone: 'neutral',
					sub: 'check now to see whether a newer release exists'
				};
			case 'no_release':
				return {
					icon: CircleDashed,
					tone: 'neutral',
					sub: `nothing published on the ${status.channel} channel yet · ${checked}`
				};
			default:
				return { icon: CircleHelp, tone: 'neutral', sub: summary.detail ?? checked };
		}
	});
	const toneTile: Record<Tone, string> = {
		success: 'bg-status-success/12 text-status-success',
		accent: 'bg-accent/12 text-accent-light',
		warning: 'bg-status-warning/12 text-status-warning',
		danger: 'bg-status-danger/12 text-status-danger',
		neutral: 'bg-white/6 text-text-tertiary'
	};

	// What an available release changes, from what the nodes report today.
	const servers = $derived(status.nodes.filter((n) => n.role === 'server').length);
	const workers = $derived(status.nodes.length - servers);
	const currentK3s = $derived(commonVersion(status.nodes.map((n) => n.k3s_version)));
	const k3sChanges = $derived(
		!!status.latest?.k3s && !!currentK3s && status.latest.k3s !== currentK3s
	);

	// Cluster card: one line per component, mixed versions spelled out.
	const agentVersions = $derived(tallyVersions(status.nodes.map((n) => n.agent_version)));
	const coordinatorVersions = $derived(
		tallyVersions(status.nodes.filter((n) => n.role === 'server').map((n) => n.coordinator_version))
	);
	const k3sVersions = $derived(tallyVersions(status.nodes.map((n) => n.k3s_version)));

	// The update walks three stages: every node, then the platform, then a
	// verification pass. Which one is active follows the coordinator phase.
	const stageIndex = $derived.by(() => {
		switch (operation?.phase) {
			case 'reconciling-platform':
				return 1;
			case 'verifying':
				return 2;
			case 'complete':
				return 3;
			default:
				return 0;
		}
	});
	const nodeSteps = $derived(operation?.steps ?? []);
	const nodesDone = $derived(nodeSteps.filter((s) => s.phase === 'complete').length);
	const runningSteps = $derived(nodeSteps.filter((s) => s.phase === 'running'));
	const failedStep = $derived(nodeSteps.find((s) => s.phase === 'failed'));
	const stages = $derived([
		{ label: 'Nodes', sub: `${nodesDone} of ${nodeSteps.length}` },
		{ label: 'Platform', sub: 'skalid and console' },
		{ label: 'Verify', sub: 'every node reports back' }
	]);
	const nowLine = $derived.by(() => {
		if (kind === 'failed') return null;
		if (stageIndex === 0) {
			if (runningSteps.length === 0) return 'Preparing the first node.';
			const names = runningSteps.map((s) => s.node).join(', ');
			return `Upgrading ${names}: the host agent and Kubernetes move to the new release.`;
		}
		if (stageIndex === 1) {
			return 'Moving skalid and the console to the new release. The console disconnects briefly.';
		}
		return 'Checking that every node reports the new release.';
	});

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
</script>

<svelte:head>
	<title>Updates — skali</title>
</svelte:head>

<PageHeader title="Software update">
	{#snippet subtitle()}
		<span>One release for the platform and every node</span>
	{/snippet}
	{#snippet actions()}
		<Button variant="secondary" onclick={() => openDetails()}>Details</Button>
	{/snippet}
</PageHeader>

<div class="grid grid-cols-1 gap-3.5 pb-6 @4xl:grid-cols-2">
	{#if disconnected}
		<p class="text-status-warning flex items-center gap-2 text-md @4xl:col-span-2" role="status">
			<LoaderCircle class="size-4 animate-spin" />
			Reconnecting to the platform. An accepted update continues in the background.
		</p>
	{/if}
	{#if feedFailure}
		<div
			class="border-status-warning/25 bg-status-warning/10 flex items-start gap-3 rounded-[13px] border p-4 @4xl:col-span-2"
			role="status"
		>
			<span class="text-status-warning mt-0.5 flex-none">
				{#if feedFailure.offline}<CloudOff size={17} />{:else}<TriangleAlert size={17} />{/if}
			</span>
			<div class="min-w-0">
				<p class="text-status-warning text-md font-medium">{feedFailure.title}</p>
				<p class="text-text-muted mt-1 text-md">{feedFailure.hint}</p>
				<button
					class="text-accent-nav mt-2 cursor-pointer text-md underline underline-offset-4"
					onclick={() => openDetails()}>View details</button
				>
			</div>
		</div>
	{/if}

	<!-- The hero: glyph, sentence, action. Below it, whichever block the
	     state calls for: what the release brings, or how the update is going. -->
	<Card class="p-5 @4xl:col-span-2">
		<div class="flex flex-col items-start gap-4 sm:flex-row sm:items-center">
			{#snippet glyph()}
				{@const Icon = hero.icon}
				<Icon size={22} strokeWidth={1.75} class={hero.spin ? 'animate-spin' : ''} />
			{/snippet}
			<div class="grid size-11.5 flex-none place-items-center rounded-[13px] {toneTile[hero.tone]}">
				{@render glyph()}
			</div>
			<div class="min-w-0 flex-1">
				<h2 class="text-text-primary text-xl font-semibold">{presentation.title}</h2>
				<p class="text-text-muted mt-0.5 text-md">{hero.sub}</p>
			</div>
			<div class="flex flex-none items-center gap-2">
				{#if summary.action === 'update'}
					<Button variant="ghost" onclick={scan} busy={busy === 'scan'} disabled={busy != null}>
						Check again
					</Button>
					<Button
						variant="primary"
						onclick={apply}
						busy={busy === 'apply'}
						disabled={busy != null || !canUpdate}
						title={updateTitle}
					>
						Update now
					</Button>
				{:else if summary.action === 'finish'}
					<Button
						variant="primary"
						onclick={apply}
						busy={busy === 'apply'}
						disabled={busy != null || !canUpdate}
						title={updateTitle}
					>
						Finish update
					</Button>
				{:else if summary.action === 'retry'}
					<Button
						variant="primary"
						onclick={resume}
						busy={busy === 'resume'}
						disabled={busy != null || !canUpdate}
						title="Continue the remaining update steps"
					>
						Retry
					</Button>
				{:else if !running}
					<Button variant="secondary" onclick={scan} busy={busy === 'scan'} disabled={busy != null}>
						Check for updates
					</Button>
				{/if}
			</div>
		</div>

		<!-- The reason updating is unavailable often is the status detail
		     itself; say it once. -->
		{#if updateTitle && !running && summary.action !== 'retry' && updateTitle !== summary.detail && updateTitle !== hero.sub}
			<p class="text-text-muted mt-3 text-md">{updateTitle}</p>
		{/if}

		{#if kind === 'available' && status.latest}
			{@const release = status.latest}
			<div class="border-border-subtle mt-5 border-t pt-5">
				<div class="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
					<h3 class="font-mono text-text-primary text-lg font-semibold">{release.version}</h3>
					{#if release.prerelease}<Pill text="prerelease" tone="warning" />{/if}
					<span class="text-text-muted text-md">
						published {formatDateTime(release.published_at)} · {status.channel} channel
					</span>
					{#if release.url}
						<!-- eslint-disable svelte/no-navigation-without-resolve -- external release page -->
						<a
							href={release.url}
							target="_blank"
							rel="noreferrer"
							class="text-accent-nav ml-auto inline-flex items-center gap-1 text-md hover:underline"
						>
							Release notes <ExternalLink size={13} />
						</a>
						<!-- eslint-enable svelte/no-navigation-without-resolve -->
					{/if}
				</div>
				<div class="mt-3.5 grid grid-cols-1 gap-2.5 @xl:grid-cols-3">
					<div class="rounded-[11px] bg-white/3 px-3.5 py-3">
						<div class="text-text-faint text-xs font-semibold tracking-wider uppercase">
							Platform
						</div>
						<div
							class="font-mono text-text-secondary mt-1.5 flex flex-wrap items-center gap-1.5 text-md"
						>
							<span class="text-text-faint">{summary.converged_version ?? 'mixed'}</span>
							<span class="inline-flex items-center gap-1.5 whitespace-nowrap">
								<ArrowRight size={12} class="text-text-ghost" />
								<span class="text-text-primary">{release.version}</span>
							</span>
						</div>
						<div class="text-text-muted mt-1 text-sm">skalid, console, host agents</div>
					</div>
					<div class="rounded-[11px] bg-white/3 px-3.5 py-3">
						<div class="text-text-faint text-xs font-semibold tracking-wider uppercase">
							Kubernetes
						</div>
						{#if k3sChanges}
							<div
								class="font-mono text-text-secondary mt-1.5 flex flex-wrap items-center gap-1.5 text-md"
							>
								<span class="text-text-faint">{currentK3s}</span>
								<span class="inline-flex items-center gap-1.5 whitespace-nowrap">
									<ArrowRight size={12} class="text-text-ghost" />
									<span class="text-text-primary">{release.k3s}</span>
								</span>
							</div>
							<div class="text-text-muted mt-1 text-sm">each node drains and rejoins</div>
						{:else}
							<div class="font-mono text-text-secondary mt-1.5 text-md">
								{currentK3s ?? release.k3s ?? 'not reported'}
							</div>
							<div class="text-text-muted mt-1 text-sm">
								{release.k3s ? 'unchanged by this release' : 'pin not published'}
							</div>
						{/if}
					</div>
					<div class="rounded-[11px] bg-white/3 px-3.5 py-3">
						<div class="text-text-faint text-xs font-semibold tracking-wider uppercase">Nodes</div>
						<div class="font-mono text-text-primary mt-1.5 text-md">
							{status.nodes.length}
						</div>
						<div class="text-text-muted mt-1 text-sm">
							{servers} controller{servers === 1 ? '' : 's'} first, then {workers} worker{workers ===
							1
								? ''
								: 's'}
						</div>
					</div>
				</div>
			</div>
		{/if}

		{#if running || kind === 'failed'}
			<div class="border-border-subtle mt-5 border-t pt-5">
				<!-- Stage strip: where in the three stages the update is. -->
				<ol class="flex items-start gap-3">
					{#each stages as stage, i (stage.label)}
						{@const done = i < stageIndex}
						{@const active = i === stageIndex}
						{@const failedHere = active && kind === 'failed'}
						<li class="flex min-w-0 flex-1 items-start gap-2.5">
							<span
								class="mt-px grid size-5.5 flex-none place-items-center rounded-full text-2xs font-semibold {done
									? 'bg-status-success/15 text-status-success'
									: failedHere
										? 'bg-status-danger/15 text-status-danger'
										: active
											? 'bg-accent/15 text-accent-light'
											: 'bg-white/6 text-text-faint'}"
							>
								{#if done}
									<Check size={12} strokeWidth={2.5} />
								{:else if failedHere}
									<TriangleAlert size={11} strokeWidth={2.25} />
								{:else if active}
									<LoaderCircle size={12} class="animate-spin" />
								{:else}
									{i + 1}
								{/if}
							</span>
							<span class="flex min-w-0 flex-col">
								<span
									class="text-md font-medium {active || done
										? 'text-text-primary'
										: 'text-text-faint'}"
								>
									{stage.label}
								</span>
								<span class="text-text-muted truncate text-sm">{stage.sub}</span>
							</span>
						</li>
					{/each}
				</ol>
				<div
					class="mt-4"
					role="progressbar"
					aria-label={progress.phase}
					aria-valuemin={0}
					aria-valuemax={100}
					aria-valuenow={progress.percent}
				>
					<ProgressBar
						pct={progress.percent}
						size="md"
						active={running}
						class={kind === 'failed' ? 'bg-status-danger' : 'bg-accent'}
					/>
					<div class="text-text-muted mt-2 flex justify-between gap-4 text-md" aria-live="polite">
						<span>{kind === 'failed' ? 'Stopped' : progress.phase}</span>
						<span>{progress.done} of {progress.total} steps · {progress.percent}%</span>
					</div>
				</div>
				{#if kind === 'failed'}
					<div
						class="border-status-danger/25 bg-status-danger/8 mt-4 flex items-start gap-3 rounded-[11px] border px-3.5 py-3"
					>
						<TriangleAlert size={16} class="text-status-danger mt-0.5 flex-none" />
						<div class="min-w-0 text-md">
							<p class="text-text-primary font-medium">
								{#if failedStep}
									<span class="font-mono">{failedStep.node}</span> did not finish upgrading
								{:else}
									The update stopped
								{/if}
							</p>
							<p class="text-text-muted mt-1 break-words">
								{failedStep?.error ?? operation?.error ?? 'No error was recorded.'}
							</p>
							<button
								class="text-accent-nav mt-2 cursor-pointer underline underline-offset-4"
								onclick={() => openDetails(failedStep?.node_id)}
							>
								View the node
							</button>
						</div>
					</div>
				{:else if nowLine}
					<p class="text-text-secondary mt-4 flex items-start gap-2.5 text-md">
						<span class="bg-status-warning mt-1.5 size-2 flex-none animate-pulse rounded-full"
						></span>
						<span>{nowLine}</span>
					</p>
				{/if}
			</div>
		{/if}
	</Card>

	<Card class="p-5">
		<h2 class="text-text-primary text-xl font-semibold">Automatic updates</h2>
		<p class="text-text-muted mt-1 text-md">
			skalid checks the release feed once a day{status.last_checked_at
				? `, last ${relativeTime(status.last_checked_at)}`
				: ''}.
		</p>
		<div class="mt-4 flex flex-col">
			<div class="border-border-subtle border-b py-3.5">
				<Toggle
					checked={status.auto_update}
					label="Install updates automatically"
					description="Install new releases for the whole cluster after the daily check. Incomplete updates require your attention."
					disabled={busy != null}
					onchange={(checked) => saveSettings(status.channel, checked)}
				/>
			</div>
			<div class="flex flex-wrap items-center gap-x-4 gap-y-2.5 py-3.5">
				<div class="flex min-w-0 flex-1 flex-col">
					<span class="text-text-secondary text-base font-medium">Channel</span>
					<span class="text-text-muted text-md leading-relaxed">
						{status.channel === 'beta'
							? 'Beta includes alpha, beta, and release candidates.'
							: 'Stable includes stable releases only. Choose beta for alpha releases.'}
					</span>
				</div>
				<div
					class="border-border-strong inline-flex flex-none rounded-[10px] border p-0.5"
					role="radiogroup"
					aria-label="Release channel"
				>
					{#each ['stable', 'beta'] as const as channel (channel)}
						<button
							type="button"
							role="radio"
							disabled={busy != null}
							aria-checked={status.channel === channel}
							onclick={() =>
								channel !== status.channel && saveSettings(channel, status.auto_update)}
							class="cursor-pointer rounded-[8px] px-3 py-1.25 text-md font-medium transition-colors disabled:cursor-default {status.channel ===
							channel
								? 'bg-accent/15 text-accent-nav'
								: 'text-text-tertiary hover:bg-white/4 hover:text-text-primary'}"
						>
							{channel}
						</button>
					{/each}
				</div>
			</div>
		</div>
	</Card>

	<Card class="p-5">
		<div class="flex items-center gap-2.5">
			<h2 class="text-text-primary text-xl font-semibold">Cluster</h2>
			<span class="text-text-muted text-md">
				{status.nodes.length} node{status.nodes.length === 1 ? '' : 's'}
			</span>
			<Button size="sm" variant="ghost" class="ml-auto" onclick={() => openDetails()}>
				Node details
			</Button>
		</div>
		<div class="mt-3">
			<KeyValueRow k="Platform" v={status.installed.version} labelWidth="w-34" />
			<KeyValueRow k="Host agents" v={agentVersions} labelWidth="w-34" />
			<KeyValueRow k="Coordinators" v={coordinatorVersions} labelWidth="w-34" />
			<KeyValueRow k="Kubernetes" v={k3sVersions} labelWidth="w-34" />
			<KeyValueRow
				k="Last update"
				v={status.last_successful
					? `${status.last_successful.target_version} · ${relativeTime(
							status.last_successful.completed_at ?? status.last_successful.updated_at
						)}`
					: 'none recorded'}
				labelWidth="w-34"
			/>
		</div>
	</Card>
</div>
