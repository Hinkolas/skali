<script lang="ts">
	import ExternalLink from '@lucide/svelte/icons/external-link';

	let {
		k,
		v,
		href,
		labelWidth = 'w-30'
	}: {
		k: string;
		v: string;
		/** External page the value links to (opens in a new tab). */
		href?: string;
		labelWidth?: string;
	} = $props();
</script>

<div class="border-border-subtle flex items-baseline gap-3 border-b py-2 last:border-0">
	<div class="text-text-faint flex-none text-md {labelWidth}">{k}</div>
	<div
		class="font-mono text-text-secondary min-w-0 overflow-hidden text-md text-ellipsis whitespace-nowrap"
	>
		{#if href}
			<!-- eslint-disable svelte/no-navigation-without-resolve -- external page -->
			<a
				{href}
				target="_blank"
				rel="noreferrer"
				class="hover:text-accent-nav inline-flex items-center gap-1 hover:underline"
			>
				{v}
				<ExternalLink size={12} />
			</a>
			<!-- eslint-enable svelte/no-navigation-without-resolve -->
		{:else}
			{v}
		{/if}
	</div>
</div>
