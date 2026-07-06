<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'New service',
		size: 'lg'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import type { ServiceType } from '$lib/mock/types';
	import { SERVICE_KIND_META } from '$lib/service-types';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import TypeBadge from '$lib/components/ui/TypeBadge.svelte';

	let { close, projectName }: { close: (created?: boolean) => void; projectName?: string } =
		$props();

	const TYPES: { type: ServiceType; description: string }[] = [
		{ type: 'application', description: 'Deploy a container from a repo or image' },
		{ type: 'database', description: 'Managed PostgreSQL with backups' },
		{ type: 'cache', description: 'In-memory key/value store' },
		{ type: 'storage', description: 'S3-compatible object storage' }
	];

	let selected = $state<ServiceType>('application');
	let name = $state('');

	function create() {
		toast.success(`Service “${name.trim() || 'untitled'}” created`, {
			description: `${SERVICE_KIND_META[selected].label} in ${projectName ?? 'project'} — mock only.`
		});
		close(true);
	}
</script>

<ModalHeader title="New service" description="Add a service to {projectName ?? 'this project'}." />

<div class="flex flex-col gap-3.5 px-5.5 py-4">
	<div class="grid grid-cols-2 gap-2">
		{#each TYPES as entry (entry.type)}
			<button
				type="button"
				onclick={() => (selected = entry.type)}
				class="flex cursor-pointer items-center gap-2.5 rounded-xl border p-3 text-left transition-colors {selected ===
				entry.type
					? 'border-accent/50 bg-accent/8'
					: 'border-border-strong hover:bg-white/3'}"
			>
				<TypeBadge kind={entry.type} form="tile" size="md" />
				<span class="flex min-w-0 flex-col gap-0.5">
					<span class="text-text-primary text-[13px] font-semibold">
						{SERVICE_KIND_META[entry.type].label}
					</span>
					<span class="text-text-faint text-[11px] leading-snug">{entry.description}</span>
				</span>
			</button>
		{/each}
	</div>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Name</span>
		<input
			bind:value={name}
			type="text"
			placeholder="my-service"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 text-[13.5px] transition-colors focus:outline-none"
		/>
	</label>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" onclick={create}>Create service</Button>
</div>
