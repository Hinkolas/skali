<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Delete project',
		archetype: 'danger'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { goto, invalidateAll } from '$app/navigation';
	import { resolve } from '$app/paths';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import { api, ApiError } from '$lib/api/client';
	import type { Project } from '$lib/types/project';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import TextInput from '$lib/components/ui/TextInput.svelte';

	// The stop moment before the one-way door: hazard anatomy, the
	// consequence stated, the project's name typed before the button arms.
	let { project, close }: { project: Project; close: (deleted?: boolean) => void } = $props();

	let typed = $state('');
	let busy = $state(false);
	let errorMessage = $state('');

	const armed = $derived(typed === project.name);

	async function remove() {
		if (!armed || busy) return;
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

<div class="px-5.5 pt-5 pb-4.5">
	<div class="flex items-center gap-3">
		<div
			class="border-status-danger/25 bg-status-danger/10 text-status-danger flex size-8.5 flex-none items-center justify-center rounded-[10px] border"
		>
			<TriangleAlert size={17} strokeWidth={1.75} />
		</div>
		<h2 class="text-text-primary text-lg font-semibold tracking-tight">
			Delete {project.display_name || project.name}?
		</h2>
	</div>

	<p class="text-text-muted mt-2 text-base leading-relaxed">
		Removes the project with its environments, values, revisions, and history. This cannot be
		undone.
	</p>

	<label class="mt-3.5 flex flex-col gap-1.5">
		<span class="text-text-tertiary text-md">
			Type <span class="font-mono text-text-secondary">{project.name}</span> to confirm
		</span>
		<TextInput
			bind:value={typed}
			mono
			autofocus
			placeholder={project.name}
			onkeydown={(e) => {
				if (e.key === 'Enter') void remove();
			}}
		/>
		{#if errorMessage}
			<span class="text-status-danger text-md">{errorMessage}</span>
		{/if}
	</label>

	<div class="mt-5 flex justify-end gap-2">
		<Button variant="ghost" disabled={busy} onclick={() => close(false)}>Cancel</Button>
		<Button
			variant="danger"
			{busy}
			disabled={!armed}
			title={armed ? undefined : `type ${project.name} to enable`}
			onclick={remove}
		>
			Delete project
		</Button>
	</div>
</div>
