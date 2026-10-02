<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'New environment'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { page } from '$app/state';
	import { api, ApiError } from '$lib/api/client';
	import { isInstanceAdmin } from '$lib/access';
	import type { AuthUser } from '$lib/types/auth';
	import type { Environment, Project } from '$lib/types/project';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Field from '$lib/components/ui/Field.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';

	let { project, close }: { project: Project; close: (created?: boolean) => void } = $props();

	let name = $state('');
	let priority = $state<'normal' | 'high'>('normal');
	let busy = $state(false);
	let errorMessage = $state('');

	// Only instance admins create high priority environments (a cluster-wide
	// resource decision); everyone else gets normal without the choice.
	const instanceAdmin = $derived(isInstanceAdmin(page.data.user as AuthUser | null));

	async function create() {
		const trimmed = name.trim();
		if (!trimmed) {
			errorMessage = 'A name is required.';
			return;
		}
		busy = true;
		errorMessage = '';
		try {
			await api.post<{ environment: Environment }>(`/v1/projects/${project.id}/environments`, {
				name: trimmed,
				priority: instanceAdmin ? priority : 'normal'
			});
			toast.success(`Environment "${trimmed}" created`);
			await invalidateAll();
			close(true);
		} catch (err) {
			errorMessage = err instanceof ApiError ? err.message : 'Could not create the environment.';
		} finally {
			busy = false;
		}
	}
</script>

<ModalHeader
	title="New environment"
	description="Environments of {project.display_name ||
		project.name} deploy the same services with their own values."
/>

<div class="flex flex-col gap-3.5 px-5.5 py-4">
	<Field label="Name" error={errorMessage} description="Lowercase letters, digits, and dashes.">
		<TextInput
			bind:value={name}
			placeholder="staging"
			invalid={errorMessage !== ''}
			onkeydown={(e) => {
				if (e.key === 'Enter') void create();
			}}
		/>
	</Field>
	{#if instanceAdmin}
		<Field
			label="Priority"
			description={priority === 'high'
				? 'High keeps running when resources are tight and starts with a read ceiling for inheriting members; consider promote-only afterwards.'
				: 'Normal yields to high priority environments when resources are tight.'}
		>
			<div class="flex gap-2">
				{#each ['normal', 'high'] as const as p (p)}
					<button
						type="button"
						onclick={() => (priority = p)}
						class="flex-1 cursor-pointer rounded-[11px] border px-3 py-2.5 text-lg font-medium transition-colors {priority ===
						p
							? 'border-accent/50 bg-accent/10 text-accent-nav'
							: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
					>
						{p}
					</button>
				{/each}
			</div>
		</Field>
	{/if}
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" {busy} onclick={create}>Create environment</Button>
</div>
