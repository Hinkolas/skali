<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Add container'
	} satisfies ModalOptions;

	export interface CreateContainerResult {
		nodeId: string;
		request: ContainerCreateRequest;
	}
</script>

<script lang="ts">
	import type { ContainerCreateRequest, ContainerKind } from '$lib/types/nodes';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';

	// Input collection only — the sudo-gated create call runs in the page
	// AFTER this modal closes, so the reauth modal (single modal slot) is
	// never displaced and the (possibly minutes-long) image pull doesn't hold
	// a modal open.
	let {
		nodes,
		initialNodeId = null,
		close
	}: {
		nodes: { id: string; name: string }[];
		/** Preselected target (e.g. the page's active node filter). */
		initialNodeId?: string | null;
		close: (result?: CreateContainerResult) => void;
	} = $props();

	// The picked node; until the user clicks one, the page's active filter
	// (when valid) or the first node is preselected.
	let pickedNodeId = $state<string | null>(null);
	const nodeId = $derived(
		pickedNodeId ??
			(initialNodeId && nodes.some((n) => n.id === initialNodeId)
				? initialNodeId
				: (nodes[0]?.id ?? ''))
	);
	let name = $state('');
	let image = $state('');
	let kind = $state<ContainerKind>('application');
	let hostPort = $state('');
	let containerPort = $state('');
	let pull = $state<'if-missing' | 'always' | 'never'>('if-missing');

	const kindHint: Record<ContainerKind, string> = {
		application: 'a project workload',
		database: 'a database engine pool',
		system: 'skali infrastructure'
	};

	const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/;
	const portsValid = $derived.by(() => {
		if (containerPort === '' && hostPort === '') return true;
		if (containerPort === '') return false; // a host port alone maps nothing
		const cp = Number(containerPort);
		if (!Number.isInteger(cp) || cp < 1 || cp > 65535) return false;
		if (hostPort === '') return true;
		const hp = Number(hostPort);
		return Number.isInteger(hp) && hp >= 1 && hp <= 65535;
	});
	const valid = $derived(
		nodeId !== '' && NAME_RE.test(name.trim()) && image.trim() !== '' && portsValid
	);

	function submit() {
		if (!valid) return;
		const request: ContainerCreateRequest = { name: name.trim(), image: image.trim(), kind, pull };
		if (containerPort !== '') {
			request.ports = [
				{
					container_port: Number(containerPort),
					...(hostPort !== '' ? { host_port: Number(hostPort) } : {})
				}
			];
		}
		close({ nodeId, request });
	}
</script>

<ModalHeader
	title="Add container"
	description="Runs a raw container on a node — the low-level admin surface, not an application deploy."
/>

<form
	class="flex flex-col gap-3.5 px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		submit();
	}}
>
	<div class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Node</span>
		<div class="flex max-h-40 flex-col gap-2 overflow-y-auto">
			{#each nodes as n (n.id)}
				{@const active = nodeId === n.id}
				<button
					type="button"
					onclick={() => (pickedNodeId = n.id)}
					class="flex cursor-pointer items-center rounded-[10px] border px-3 py-2.5 text-[13px] font-medium transition-colors {active
						? 'border-accent/50 bg-accent/10 text-accent-nav'
						: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
				>
					{n.name}
				</button>
			{/each}
		</div>
	</div>

	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Name</span>
		<input
			bind:value={name}
			type="text"
			required
			placeholder="nginx-test"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 font-mono text-[13px] transition-colors focus:outline-none"
		/>
	</label>

	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Image</span>
		<input
			bind:value={image}
			type="text"
			required
			placeholder="nginx:alpine"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 font-mono text-[13px] transition-colors focus:outline-none"
		/>
	</label>

	<div class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Kind</span>
		<div class="flex flex-col gap-2">
			{#each ['application', 'database', 'system'] as const as k (k)}
				{@const active = kind === k}
				<button
					type="button"
					onclick={() => (kind = k)}
					class="flex cursor-pointer items-center justify-between rounded-[10px] border px-3 py-2.5 text-[13px] font-medium transition-colors {active
						? 'border-accent/50 bg-accent/10 text-accent-nav'
						: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
				>
					<span>{k}</span>
					<span class="text-text-ghost text-[12px] font-normal">{kindHint[k]}</span>
				</button>
			{/each}
		</div>
	</div>

	<div class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Port mapping</span>
		<div class="flex items-center gap-2">
			<input
				bind:value={hostPort}
				type="text"
				inputmode="numeric"
				placeholder="host (auto)"
				class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 font-mono text-[13px] transition-colors focus:outline-none"
			/>
			<span class="text-text-ghost flex-none text-[13px]">→</span>
			<input
				bind:value={containerPort}
				type="text"
				inputmode="numeric"
				placeholder="container"
				class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 font-mono text-[13px] transition-colors focus:outline-none"
			/>
		</div>
		<p class="text-text-ghost text-[12px] leading-relaxed">
			Optional. Leave the host port empty to publish on an ephemeral port.
		</p>
	</div>

	<div class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Image pull</span>
		<div class="flex gap-2">
			{#each ['if-missing', 'always', 'never'] as const as p (p)}
				{@const active = pull === p}
				<button
					type="button"
					onclick={() => (pull = p)}
					class="flex-1 cursor-pointer rounded-[10px] border px-3 py-2.5 text-[13px] font-medium transition-colors {active
						? 'border-accent/50 bg-accent/10 text-accent-nav'
						: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
				>
					{p}
				</button>
			{/each}
		</div>
		<p class="text-text-ghost text-[12px] leading-relaxed">
			Pulling a missing image can take a while; the container appears once it's ready.
		</p>
	</div>

	<!-- Hidden submit so Enter works; the visible buttons live in the footer. -->
	<button type="submit" class="hidden" aria-hidden="true"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(undefined)}>Cancel</Button>
	<Button variant="primary" disabled={!valid} onclick={submit}>Create container</Button>
</div>
