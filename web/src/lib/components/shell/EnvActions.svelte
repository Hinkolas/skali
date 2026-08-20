<script lang="ts">
	import { page } from '$app/state';
	import { requiredTitle, roleAtLeast } from '$lib/access';
	import type { Environment, Project } from '$lib/types/project';
	import { modal } from '$lib/stores/modal.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import PromoteModal, {
		modalOptions as promoteModalOptions
	} from '$lib/components/run/PromoteModal.svelte';
	import RedeployModal, {
		modalOptions as redeployModalOptions
	} from '$lib/components/run/RedeployModal.svelte';

	// Environment-wide actions in the topbar: they act on the environment the
	// breadcrumb names, so they live next to it and stay reachable from every
	// page of the project. Deep eligibility (something running, per-target
	// refusals) belongs to the modals and the server; the buttons only gate
	// what they can see cheaply.
	const data = $derived(
		page.data as {
			project?: Project;
			environments?: Environment[];
			env?: Environment | null;
		}
	);

	const env = $derived(data.env ?? null);
	const environments = $derived(data.environments ?? []);
	const mayDeploy = $derived(roleAtLeast(env?.access, 'deploy'));

	const promoteTitle = $derived(
		environments.length < 2 ? 'no other environments to promote to' : undefined
	);
	const redeployTitle = $derived(
		mayDeploy ? undefined : requiredTitle('deploy', 'environment', env?.name ?? '')
	);

	function openPromote() {
		if (!env) return;
		modal.open(PromoteModal, { source: env, environments }, promoteModalOptions);
	}

	function openRedeploy() {
		if (!env) return;
		modal.open(RedeployModal, { env }, redeployModalOptions);
	}
</script>

{#if data.project && env && env.access !== 'none'}
	<div class="flex items-center gap-2">
		<Button
			size="sm"
			class="h-8"
			disabled={!!promoteTitle}
			title={promoteTitle}
			onclick={openPromote}
		>
			Promote
		</Button>
		<Button
			variant="primary"
			size="sm"
			class="h-8"
			disabled={!!redeployTitle}
			title={redeployTitle}
			onclick={openRedeploy}
		>
			Redeploy
		</Button>
	</div>
{/if}
