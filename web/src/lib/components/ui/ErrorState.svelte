<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import Compass from '@lucide/svelte/icons/compass';
	import Lock from '@lucide/svelte/icons/lock';
	import ServerCrash from '@lucide/svelte/icons/server-crash';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import Button from './Button.svelte';

	// Shared body of the two +error.svelte boundaries. Reads status/message
	// straight from page state; the boundary only chooses the frame around it.
	let { showIcon = true }: { showIcon?: boolean } = $props();

	const details = $derived.by(() => {
		const status = page.status;
		if (status === 404)
			return {
				icon: Compass,
				tint: 'bg-white/4 text-text-ghost',
				text: "The page you're looking for doesn't exist or has been moved."
			};
		if (status === 401)
			return {
				icon: Lock,
				tint: 'bg-status-warning/10 text-status-warning',
				text: 'You need to sign in to view this page.'
			};
		if (status === 403)
			return {
				icon: Lock,
				tint: 'bg-status-warning/10 text-status-warning',
				text: "You don't have permission to view this page."
			};
		if (status >= 500)
			return {
				icon: ServerCrash,
				tint: 'bg-status-danger/10 text-status-danger',
				text: 'The server hit an unexpected problem. Try again in a moment.'
			};
		return {
			icon: TriangleAlert,
			tint: 'bg-white/4 text-text-ghost',
			text: 'Something unexpected happened.'
		};
	});
</script>

<div class="flex flex-col items-center text-center">
	{#if showIcon}
		{@const Icon = details.icon}
		<div class="mb-5 grid size-12 place-items-center rounded-2xl {details.tint}">
			<Icon size={24} strokeWidth={1.75} />
		</div>
	{/if}

	<div class="font-mono text-text-primary text-6xl leading-none font-semibold tracking-[-0.02em]">
		{page.status}
	</div>
	<h1 class="text-text-primary mt-3 text-xl font-semibold">
		{page.error?.message ?? 'Something went wrong'}
	</h1>
	<p class="text-text-muted mt-1.5 max-w-[396px] text-base">{details.text}</p>

	<div class="mt-6 flex items-center gap-2.5">
		<Button variant="secondary" onclick={() => history.back()}>Go back</Button>
		<Button variant="primary" href={resolve('/')}>Go to home</Button>
	</div>

	<div class="font-mono text-text-ghost mt-7 text-xs">{page.url.pathname}</div>
</div>
