<script lang="ts">
	// Inline editor for one environment's access ceiling, deploy policy,
	// promotion sources, and priority. Saves a diff with PATCH; the reauth
	// interceptor handles sudo. Rendered on the settings page (not in a
	// modal) for that reason.
	import { invalidateAll } from '$app/navigation';
	import { api, ApiError } from '$lib/api/client';
	import { ROLES, ROLE_HINT, requiredTitle } from '$lib/access';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Field from '$lib/components/ui/Field.svelte';
	import RolePicker, { type RoleOption } from './RolePicker.svelte';
	import type { AccessRole, Environment, EnvironmentSettings } from '$lib/types/project';

	let {
		environment,
		environments,
		canEdit,
		instanceAdmin,
		onclose
	}: {
		environment: Environment;
		/** All environments of the project, for the promotion sources. */
		environments: Environment[];
		canEdit: boolean;
		instanceAdmin: boolean;
		onclose?: () => void;
	} = $props();

	const settings = $derived(environment.settings as EnvironmentSettings);
	const others = $derived(environments.filter((e) => e.id !== environment.id));
	const readOnlyTitle = $derived(requiredTitle('admin', 'environment', environment.name));

	// Writable derived: resets when the environment data changes (save,
	// env switch), stays editable in between.
	let maxRole = $derived(settings.max_role);
	let deployPolicy = $derived(settings.deploy_policy);
	let promoteFrom = $derived<string[]>([...settings.promote_from]);
	let priority = $derived(settings.priority);
	let saving = $state(false);

	const ceilingOptions: RoleOption[] = ROLES.map((r) => ({ value: r, hint: ROLE_HINT[r] }));
	const policyOptions: RoleOption[] = [
		{ value: 'direct', hint: 'Deploys land directly; promote and rollback work too.' },
		{
			value: 'promote-only',
			hint: 'Only promotions from the sources below, rollbacks, or an admin bypass change the running revision.'
		}
	];
	const priorityOptions = $derived<RoleOption[]>([
		{ value: 'normal', hint: 'Yields to high priority environments when resources are tight.' },
		{
			value: 'high',
			hint: instanceAdmin
				? 'Keeps running when resources are tight; starts read-only for inheriting members.'
				: 'Instance admins only.'
		}
	]);

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
			toast.success(`Updated ${environment.name}`);
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : 'Could not update the environment');
		} finally {
			saving = false;
		}
	}
</script>

<div
	class="border-border-subtle bg-surface-base/40 mt-2 mb-1 flex flex-col gap-3.5 rounded-xl border p-4"
>
	<div class="grid grid-cols-2 gap-3.5">
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
			description="High marks environments that must keep running when resources are tight."
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
			description="promote-only refuses direct deploys: tested revisions arrive by promotion."
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
					<span class="text-text-ghost font-mono text-xs">any</span>
				{/each}
			</div>
		</Field>
	</div>
	<div class="flex items-center gap-2">
		{#if canEdit}
			<Button size="sm" variant="primary" busy={saving} disabled={!dirty} onclick={save}
				>Save</Button
			>
		{/if}
		{#if onclose}
			<Button size="sm" variant="ghost" onclick={onclose}>Close</Button>
		{/if}
		{#if !canEdit}
			<span class="text-text-ghost text-xs">{readOnlyTitle} to change these.</span>
		{/if}
	</div>
</div>
