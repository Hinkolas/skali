<script lang="ts">
	import { updated } from '$app/state';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import Button from '$lib/components/ui/Button.svelte';

	// Shown once SvelteKit's version poll sees a newer bundle than the one
	// this tab runs: a platform update replaced skalid, and with it the
	// embedded console. Never reloads on its own: the operator may be in
	// the middle of a form. The updates page, which is read-only and where
	// the operator is waiting for exactly this, reloads itself instead.
	let version = $state<string | null>(null);

	// The daemon stamps Skali-Version on every response, so the notice can
	// name the release without a dedicated endpoint. Best effort.
	$effect(() => {
		if (!updated.current) return;
		fetch('/api/healthz', { signal: AbortSignal.timeout(2000) })
			.then((res) => {
				version = res.headers.get('skali-version');
			})
			.catch(() => {});
	});
</script>

{#if updated.current}
	<div
		class="border-accent/30 bg-accent/10 mb-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border px-4 py-2.5"
		role="status"
	>
		<span class="text-accent-light flex-none"><RefreshCw class="size-4" /></span>
		<p class="text-text-primary min-w-0 flex-1 text-md">
			{#if version}
				The console was updated to <span class="font-mono">{version}</span>. Reload to use the new
				version.
			{:else}
				The console was updated. Reload to use the new version.
			{/if}
		</p>
		<Button size="sm" variant="primary" onclick={() => window.location.reload()}>Reload</Button>
	</div>
{/if}
