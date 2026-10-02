<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'New project'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { api, ApiError } from '$lib/api/client';
	import type { Environment, Project } from '$lib/types/project';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Field from '$lib/components/ui/Field.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';

	let { close }: { close: (created?: boolean) => void } = $props();

	let name = $state('');
	let environment = $state('production');
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
			const { project } = await api.post<{ project: Project }>('/v1/projects', {
				name: trimmed
			});
			const envName = environment.trim();
			if (envName) {
				await api.post<{ environment: Environment }>(`/v1/projects/${project.id}/environments`, {
					name: envName
				});
			}
			toast.success(`Project "${trimmed}" created`);
			await invalidateAll();
			close(true);
			await goto(resolve('/(app)/projects/[project]', { project: project.name }));
		} catch (err) {
			errorMessage = err instanceof ApiError ? err.message : 'Could not create the project.';
			busy = false;
		}
	}
</script>

<ModalHeader
	title="New project"
	description="A project bundles applications, databases and buckets."
/>

<div class="flex flex-col gap-3.5 px-5.5 py-4">
	<Field
		label="Name"
		error={errorMessage}
		description="Lowercase letters, digits, and dashes; also the manifest name."
	>
		<TextInput
			bind:value={name}
			placeholder="my-project"
			invalid={errorMessage !== ''}
			onkeydown={(e) => {
				if (e.key === 'Enter') void create();
			}}
		/>
	</Field>
	<Field label="Initial environment" description="Leave empty to add environments later.">
		<TextInput bind:value={environment} placeholder="production" />
	</Field>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" {busy} onclick={create}>Create project</Button>
</div>
