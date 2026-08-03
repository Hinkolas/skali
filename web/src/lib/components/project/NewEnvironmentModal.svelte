<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'New environment'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { invalidateAll } from '$app/navigation';
	import { api, ApiError } from '$lib/api/client';
	import type { Environment, Project } from '$lib/types/project';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Field from '$lib/components/ui/Field.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';

	let { project, close }: { project: Project; close: (created?: boolean) => void } = $props();

	let name = $state('');
	let busy = $state(false);
	let errorMessage = $state('');

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
				name: trimmed
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
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" {busy} onclick={create}>Create environment</Button>
</div>
