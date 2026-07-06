<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Add node'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	import { ASSIGNABLE_NODE_ROLES, type NodeRole } from '$lib/types/nodes';
	import Button from '$lib/components/ui/Button.svelte';

	// Role picker only — the sudo-gated mint call runs in the page AFTER this
	// modal closes, so the reauth modal (single modal slot) is never displaced.
	let { close }: { close: (roles?: NodeRole[]) => void } = $props();

	let roles = $state<NodeRole[]>(['worker']);

	function toggle(role: NodeRole) {
		roles = roles.includes(role) ? roles.filter((r) => r !== role) : [...roles, role];
	}

	const roleHint: Record<NodeRole, string> = {
		worker: 'runs application containers',
		edge: 'terminates public ingress',
		builder: 'builds images',
		master: '' // never offered
	};
</script>

<div class="px-5.5 pt-5 pb-2">
	<h2 class="text-text-primary text-[16px] font-semibold tracking-tight">Add node</h2>
	<p class="text-text-muted mt-1 text-[13px]">
		Pick the roles the new node should hold. You get a one-time join token and the command to run
		on the machine.
	</p>
</div>

<div class="flex flex-col gap-1.5 px-5.5 py-4">
	<span class="text-text-tertiary text-[12.5px] font-medium">Roles</span>
	<div class="flex flex-col gap-2">
		{#each ASSIGNABLE_NODE_ROLES as role (role)}
			{@const active = roles.includes(role)}
			<button
				type="button"
				onclick={() => toggle(role)}
				class="flex cursor-pointer items-center justify-between rounded-[10px] border px-3 py-2.5 text-[13px] font-medium transition-colors {active
					? 'border-accent/50 bg-accent/10 text-accent-nav'
					: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
			>
				<span>{role}</span>
				<span class="text-text-ghost text-[11.5px] font-normal">{roleHint[role]}</span>
			</button>
		{/each}
	</div>
	<p class="text-text-ghost mt-1 text-[11.5px]">
		Roles can be changed later. The master role is fixed to this control-plane node.
	</p>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(undefined)}>Cancel</Button>
	<Button variant="primary" disabled={roles.length === 0} onclick={() => close(roles)}>
		Generate join token
	</Button>
</div>
