<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'New project'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';

	let { close }: { close: (created?: boolean) => void } = $props();

	let name = $state('');
	let environment = $state<'production' | 'staging'>('production');

	function create() {
		toast.success(`Project “${name.trim() || 'untitled'}” created`, {
			description: 'Mock only — nothing was actually deployed.'
		});
		close(true);
	}
</script>

<div class="px-5.5 pt-5 pb-2">
	<h2 class="text-text-primary text-[16px] font-semibold tracking-tight">New project</h2>
	<p class="text-text-muted mt-1 text-[13px]">
		A project bundles applications, databases and volumes.
	</p>
</div>

<div class="flex flex-col gap-3.5 px-5.5 py-4">
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Name</span>
		<input
			bind:value={name}
			type="text"
			placeholder="my-project"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 text-[13.5px] transition-colors focus:outline-none"
		/>
	</label>
	<div class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Environment</span>
		<div class="flex gap-2">
			{#each ['production', 'staging'] as const as env (env)}
				<button
					type="button"
					onclick={() => (environment = env)}
					class="flex-1 cursor-pointer rounded-[10px] border px-3 py-2.5 text-[13px] font-medium transition-colors {environment ===
					env
						? 'border-accent/50 bg-accent/10 text-accent-nav'
						: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
				>
					{env}
				</button>
			{/each}
		</div>
	</div>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" onclick={create}>Create project</Button>
</div>
