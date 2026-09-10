<script lang="ts">
	import { formatDateTime } from '$lib/format';
	let { fields, live = false }: { fields: Record<string, unknown>; live?: boolean } = $props();
	let now = $state(Date.now());
	$effect(() => {
		if (!live) return;
		const timer = setInterval(() => {
			now = Date.now();
		}, 1000);
		return () => clearInterval(timer);
	});
	const value = (key: string): string => (fields[key] == null ? '' : String(fields[key]));
	const phase = $derived(value('phase'));
	const retry = $derived(Date.parse(value('next_retry_at')));
	const remaining = $derived(Math.max(0, Math.ceil((retry - now) / 1000)));
	const countdown = $derived(
		remaining >= 3600
			? `${Math.floor(remaining / 3600)}h ${Math.floor((remaining % 3600) / 60)}m`
			: remaining >= 60
				? `${Math.floor(remaining / 60)}m ${remaining % 60}s`
				: `${remaining}s`
	);
	const labels: Record<string, string> = {
		pending: 'Waiting for issuance',
		issuing: 'Issuing certificate',
		backoff: 'Waiting to retry',
		active: 'Certificate is valid'
	};
	const detailRows = [
		['certificate', 'Certificate'],
		['namespace', 'Namespace'],
		['secret', 'TLS secret'],
		['next_private_key_secret', 'Next key secret'],
		['request', 'Certificate request'],
		['request_status', 'Request status'],
		['order', 'ACME order'],
		['order_state', 'Order state'],
		['order_reason', 'Order failure'],
		['challenges', 'Validation challenges']
	];
</script>

<div class="text-base min-w-0 space-y-3 py-2">
	<div>
		<p class="text-text-primary font-medium break-words">{value('domain') || 'TLS certificate'}</p>
		<p class="text-text-secondary mt-1">
			{labels[phase] ?? phase}{#if value('issuance_attempt')}
				· Attempt {value('issuance_attempt')}{/if}
		</p>
	</div>
	{#if value('failure')}
		<p class="text-status-danger break-words">{value('failure')}</p>
	{/if}
	<dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5">
		{#if Number(fields.failed_attempts) > 0}<dt class="text-text-muted">Failed attempts</dt>
			<dd class="text-text-primary tabular-nums">{value('failed_attempts')}</dd>{/if}
		{#if value('last_failure_at')}<dt class="text-text-muted">Last failure</dt>
			<dd class="text-text-secondary">{formatDateTime(value('last_failure_at'))}</dd>{/if}
		{#if Number.isFinite(retry)}
			<dt class="text-text-muted">Next attempt</dt>
			<dd class="text-text-primary">
				{value('next_attempt')}{#if live}
					· {remaining > 0 ? `in about ${countdown}` : 'due; waiting for cert-manager'}{/if}
			</dd>
			<dt class="text-text-muted">Estimated retry</dt>
			<dd class="text-text-secondary">{formatDateTime(value('next_retry_at'))}</dd>
		{/if}
		{#if value('valid_until')}<dt class="text-text-muted">Valid until</dt>
			<dd class="text-text-secondary">{formatDateTime(value('valid_until'))}</dd>{/if}
		{#if phase !== 'active' && value('deadline')}<dt class="text-text-muted">Rollout deadline</dt>
			<dd class="text-text-secondary">{formatDateTime(value('deadline'))}</dd>{/if}
	</dl>
	{#if value('reason') || value('message')}
		<div class="text-text-secondary break-words [overflow-wrap:anywhere]">
			<p class="text-text-primary font-medium">{value('reason')}</p>
			<p class="mt-1 whitespace-pre-wrap">{value('message')}</p>
		</div>
	{/if}
	{#each ['recovery_error', 'observation_error'] as key (key)}
		{#if value(key)}<p class="text-status-warning break-words [overflow-wrap:anywhere]">
				{key === 'recovery_error'
					? 'Could not request a retry'
					: 'Could not read issuance details'}: {value(key)}
			</p>{/if}
	{/each}
	{#if value('guidance')}<p class="text-text-secondary">{value('guidance')}</p>{/if}
	<details class="border-border-subtle border-t pt-2">
		<summary class="text-text-secondary cursor-pointer py-1">Certificate and ACME details</summary>
		<dl class="mt-2 space-y-2">
			{#each detailRows as [key, label] (key)}
				{#if value(key)}<div>
						<dt class="text-text-muted text-md">{label}</dt>
						<dd
							class="text-text-secondary mt-0.5 whitespace-pre-wrap break-words [overflow-wrap:anywhere]"
						>
							{value(key)}
						</dd>
					</div>{/if}
			{/each}
		</dl>
	</details>
</div>
