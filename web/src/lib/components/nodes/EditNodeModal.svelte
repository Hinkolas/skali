<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Edit node'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { api, ApiError } from '$lib/api/client';
	import { toast } from '$lib/stores/toast.svelte';
	import { ASSIGNABLE_NODE_ROLES, type Node, type NodeRole } from '$lib/types/nodes';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';

	let { node, close }: { node: Node; close: (updated?: boolean) => void } = $props();

	// The modal host mounts this component fresh per open, so reading the
	// initial prop value is exactly right.
	// svelte-ignore state_referenced_locally
	const isMaster = node.roles.includes('master');

	// The modal host mounts this component fresh per open, so seeding the form
	// from the initial prop value is exactly right.
	// svelte-ignore state_referenced_locally
	let name = $state(node.name);
	// svelte-ignore state_referenced_locally
	let roles = $state<NodeRole[]>([...node.roles]);
	// svelte-ignore state_referenced_locally
	let publicAddr = $state(node.public_addr ?? '');
	let busy = $state(false);

	function toggle(role: NodeRole) {
		roles = roles.includes(role) ? roles.filter((r) => r !== role) : [...roles, role];
	}

	const rolesChanged = $derived(
		roles.length !== node.roles.length || roles.some((r) => !node.roles.includes(r))
	);
	// Master keeps at least its master role; other nodes need one role minimum.
	const valid = $derived(roles.length > 0 && name.trim() !== '');

	async function save() {
		if (busy || !valid) return;
		const patch: { name?: string; roles?: NodeRole[]; public_addr?: string } = {};
		if (name.trim() !== node.name) patch.name = name.trim();
		if (rolesChanged) patch.roles = roles;
		if (publicAddr.trim() !== (node.public_addr ?? '')) patch.public_addr = publicAddr.trim();
		if (Object.keys(patch).length === 0) {
			close(false);
			return;
		}
		busy = true;
		try {
			await api.patch(`/v1/nodes/${node.id}`, patch);
			toast.success(`Updated ${node.name}`);
			close(true);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not update the node');
			busy = false;
		}
	}
</script>

<ModalHeader title="Edit node">
	<span class="font-mono text-[12px]">{node.advertise_addr || node.id}</span>
</ModalHeader>

<form
	class="flex flex-col gap-3.5 px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		save();
	}}
>
	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Name</span>
		<input
			bind:value={name}
			type="text"
			required
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 text-[13.5px] transition-colors focus:outline-none"
		/>
	</label>

	<div class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Roles</span>
		<div class="flex gap-2">
			{#if isMaster}
				<span
					class="border-border-strong text-text-ghost flex-none cursor-default rounded-[10px] border px-3 py-2.5 text-[13px] font-medium"
					title="The master role is fixed"
				>
					master
				</span>
			{/if}
			{#each ASSIGNABLE_NODE_ROLES as role (role)}
				{@const active = roles.includes(role)}
				<button
					type="button"
					onclick={() => toggle(role)}
					class="flex-1 cursor-pointer rounded-[10px] border px-3 py-2.5 text-[13px] font-medium transition-colors {active
						? 'border-accent/50 bg-accent/10 text-accent-nav'
						: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
				>
					{role}
				</button>
			{/each}
		</div>
	</div>

	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Public address</span>
		<input
			bind:value={publicAddr}
			type="text"
			placeholder="203.0.113.10:443"
			class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.75 font-mono text-[13px] transition-colors focus:outline-none"
		/>
		<p class="text-text-ghost text-[12px] leading-relaxed">
			Where public DNS points for this node (edge role). Leave empty for private-only nodes.
		</p>
	</label>

	<!-- Hidden submit so Enter works; the visible buttons live in the footer. -->
	<button type="submit" class="hidden" aria-hidden="true"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
	<Button variant="primary" disabled={!valid} {busy} onclick={save}>Save changes</Button>
</div>
