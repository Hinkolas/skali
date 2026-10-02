<script lang="ts">
	// The Studio's S3 connection panel for a bucket on its own hostname.
	import Copy from '@lucide/svelte/icons/copy';

	const fields = [
		{ label: 'Endpoint', value: 'https://uploads.example.com', copy: true },
		{ label: 'Bucket', value: 'uploads', copy: true },
		{ label: 'Access key', value: '••••••••••••••••', copy: false }
	];
</script>

<div
	class="flex w-full flex-col gap-2.5 rounded-[15px] border border-border-raised bg-surface-card px-[19.8px] py-[18px] leading-normal text-text-primary"
>
	<div class="mb-1 flex items-center gap-2.5">
		<span class="text-[17px] font-semibold">S3 connection</span>
		<span
			class="ml-auto flex h-7.5 items-center rounded-lg border border-border-strong bg-white/2 px-[11px] text-[13px] font-medium text-text-secondary"
		>
			Reveal keypair
		</span>
	</div>
	{#each fields as field (field.label)}
		<div class="flex items-center gap-2.5">
			<span class="w-19 shrink-0 text-[13px] text-text-faint">{field.label}</span>
			<span
				class={[
					'flex min-w-0 grow items-center gap-2 rounded-lg border border-border-default bg-surface-input px-[12.1px] py-[8.8px] font-mono text-[13px]',
					field.copy ? 'text-text-secondary' : 'text-text-ghost'
				]}
			>
				<span class="grow truncate">{field.value}</span>
				{#if field.copy}<Copy size={13} class="shrink-0 text-text-ghost" />{/if}
			</span>
		</div>
	{/each}
	<div class="mt-1.5 border-t border-border-subtle pt-3 text-[13px] text-text-secondary">
		Public reads <span class="text-text-faint">· disabled · every request needs the keypair</span>
	</div>
</div>
