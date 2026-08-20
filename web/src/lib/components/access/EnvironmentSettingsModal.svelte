<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Environment settings',
		panelClass: 'flex max-h-[90dvh] w-full flex-col sm:max-w-2xl'
	} satisfies ModalOptions;
</script>

<script lang="ts">
	// Modal editor for one environment's access ceiling, deploy policy,
	// promotion sources, and priority. Saves a diff with PATCH and closes; the
	// sudo reauth prompt layers above this modal on the stack.
	import { invalidateAll } from '$app/navigation';
	import { api, ApiError } from '$lib/api/client';
	import { ROLES, ROLE_HINT, requiredTitle } from '$lib/access';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Field from '$lib/components/ui/Field.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';
	import RolePicker, { type RoleOption } from './RolePicker.svelte';
	import type { AccessRole, Environment, EnvironmentSettings } from '$lib/types/project';

	let {
		environment,
		environments,
		canEdit,
		instanceAdmin,
		close
	}: {
		environment: Environment;
		/** All environments of the project, for the promotion sources. */
		environments: Environment[];
		canEdit: boolean;
		instanceAdmin: boolean;
		close: (saved?: boolean) => void;
	} = $props();

	// svelte-ignore state_referenced_locally
	const settings = environment.settings as EnvironmentSettings;
	// svelte-ignore state_referenced_locally
	const others = environments.filter((e) => e.id !== environment.id);
	// svelte-ignore state_referenced_locally
	const readOnlyTitle = requiredTitle('admin', 'environment', environment.name);

	// Seeded once from the open-time snapshot: the modal closes on save, so it
	// never has to track a reload underneath it.
	let maxRole = $state(settings.max_role);
	let deployPolicy = $state(settings.deploy_policy);
	let promoteFrom = $state<string[]>([...settings.promote_from]);
	let priority = $state(settings.priority);
	let saving = $state(false);

	const ceilingOptions: RoleOption[] = ROLES.map((r) => ({ value: r, hint: ROLE_HINT[r] }));
	const policyOptions: RoleOption[] = [
		{ value: 'direct', hint: 'Deploys land directly; promote and rollback work too.' },
		{
			value: 'promote-only',
			hint: 'Only promotions from the sources below, rollbacks, or an environment admin with skali deploy --bypass-protection change the running revision.'
		}
	];
	// svelte-ignore state_referenced_locally
	const priorityOptions: RoleOption[] = [
		{ value: 'normal', hint: 'Yields to high priority environments when resources are tight.' },
		{
			value: 'high',
			hint: instanceAdmin
				? 'Keeps running when resources are tight; starts read-only for inheriting members.'
				: 'Instance admins only.'
		}
	];

	const dirty = $derived(
		maxRole !== settings.max_role ||
			deployPolicy !== settings.deploy_policy ||
			priority !== settings.priority ||
			promoteFrom.join(',') !== settings.promote_from.join(',')
	);

	function togglePromoteFrom(name: string) {
		promoteFrom = promoteFrom.includes(name)
			? promoteFrom.filter((n) => n !== name)
			: [...promoteFrom, name];
	}

	async function save() {
		if (!dirty || saving) return;
		const patch: Partial<{
			max_role: AccessRole;
			deploy_policy: string;
			promote_from: string[];
			priority: string;
		}> = {};
		if (maxRole !== settings.max_role) patch.max_role = maxRole;
		if (deployPolicy !== settings.deploy_policy) patch.deploy_policy = deployPolicy;
		if (promoteFrom.join(',') !== settings.promote_from.join(',')) patch.promote_from = promoteFrom;
		if (priority !== settings.priority) patch.priority = priority;
		saving = true;
		try {
			await api.patch(`/v1/environments/${environment.id}`, patch);
			toast.success(
				`Updated ${environment.name}`,
				patch.priority
					? { description: 'Application pods roll onto the new priority class.' }
					: undefined
			);
			await invalidateAll();
			close(true);
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not update the environment');
		} finally {
			saving = false;
		}
	}
</script>

<ModalHeader title="Environment settings">
	<span class="font-mono">{environment.name}</span>
	{#if !canEdit}
		· {readOnlyTitle} to change these.
	{/if}
</ModalHeader>

<div class="grid gap-x-5 gap-y-4 overflow-y-auto px-5.5 py-4 sm:grid-cols-2">
	<Field
		label="Ceiling for inherited roles"
		description="Caps the role members inherit from their project role here; explicit per-environment roles and project admins are not capped."
	>
		<RolePicker
			value={maxRole}
			options={ceilingOptions}
			label="Ceiling of {environment.name}"
			disabled={!canEdit}
			title={canEdit ? undefined : readOnlyTitle}
			onchange={(v) => (maxRole = v as AccessRole)}
		/>
	</Field>
	<Field
		label="Priority"
		description="High marks environments that must keep running when resources are tight; a change rolls the environment's application pods."
	>
		<RolePicker
			value={priority}
			options={priorityOptions}
			label="Priority of {environment.name}"
			disabled={!canEdit || (!instanceAdmin && priority !== 'high')}
			title={!canEdit
				? readOnlyTitle
				: !instanceAdmin && priority !== 'high'
					? 'instance admin required to raise an environment to high priority'
					: undefined}
			onchange={(v) => (priority = v as 'normal' | 'high')}
		/>
	</Field>
	<Field
		label="Deploy policy"
		description="promote-only refuses direct deploys: tested revisions arrive by promotion, or by a recorded admin bypass."
	>
		<RolePicker
			value={deployPolicy}
			options={policyOptions}
			label="Deploy policy of {environment.name}"
			disabled={!canEdit}
			title={canEdit ? undefined : readOnlyTitle}
			onchange={(v) => (deployPolicy = v as 'direct' | 'promote-only')}
		/>
	</Field>
	<Field
		label="Promote from"
		description={others.length
			? 'Sources promotions may come from; none selected means any environment of the project.'
			: 'No other environments yet; promotions may come from any environment of the project.'}
	>
		<div class="flex flex-wrap gap-1.5">
			{#each others as other (other.id)}
				{@const selected = promoteFrom.includes(other.name)}
				<button
					type="button"
					disabled={!canEdit}
					title={canEdit ? undefined : readOnlyTitle}
					onclick={() => togglePromoteFrom(other.name)}
					class="font-mono rounded-full border px-2.5 py-1 text-md transition-colors disabled:cursor-default {selected
						? 'border-accent/50 bg-accent/10 text-accent-nav'
						: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
				>
					{other.name}
				</button>
			{:else}
				<span class="text-text-faint font-mono text-md">any</span>
			{/each}
		</div>
	</Field>
</div>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	{#if canEdit}
		<Button variant="ghost" onclick={() => close(false)}>Cancel</Button>
		<Button variant="primary" busy={saving} disabled={!dirty} onclick={save}>Save</Button>
	{:else}
		<Button variant="secondary" onclick={() => close(false)}>Close</Button>
	{/if}
</div>
