<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Delete project'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { api, ApiError } from '$lib/api/client';
	import type { Project } from '$lib/types/project';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Field from '$lib/components/ui/Field.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';

	let { project, close }: { project: Project; close: (deleted?: boolean) => void } = $props();

	let typed = $state('');
	let busy = $state(false);
	let errorMessage = $state('');

	async function remove() {
		busy = true;
		errorMessage = '';
		try {
			await api.del(`/v1/projects/${project.id}`);
			toast.success(`Project "${project.name}" deleted`);
			close(true);
			await goto(resolve('/(app)/projects'));
			await invalidateAll();
		} catch (err) {
			errorMessage = err instanceof ApiError ? err.message : 'Could not delete the project.';
			busy = false;
		}
	}
</script>

<ModalHeader
	title="Delete {project.display_name || project.name}?"
	description="Removes the project with its environments, values, revisions, and history. This cannot be undone."
/>

<div class="flex flex-col gap-3.5 px-5.5 py-4">
	<Field label="Type the project name to confirm" error={errorMessage}>
		<TextInput bind:value={typed} placeholder={project.name} mono />
	</Field>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="danger" {busy} disabled={typed !== project.name} onclick={remove}>
		Delete project
	</Button>
</div>
