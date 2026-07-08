<script module lang="ts">
	import type { ModalOptions } from '$lib/stores/modal.svelte';

	export const modalOptions = {
		label: 'Workload'
	} satisfies ModalOptions;

	/** The request body the page sends (POST when creating, PATCH when editing). */
	export interface WorkloadModalResult {
		body: Record<string, unknown>;
	}
</script>

<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import X from '@lucide/svelte/icons/x';
	import type { Workload, WorkloadSpec } from '$lib/types/workloads';
	import Button from '$lib/components/ui/Button.svelte';
	import ModalHeader from '$lib/components/ui/ModalHeader.svelte';

	// Input collection only — the sudo-gated call runs in the page AFTER this
	// modal closes, so the reauth modal (single modal slot) is never displaced.
	let {
		nodes,
		workload = null,
		close
	}: {
		nodes: { id: string; name: string }[];
		/** null = create; otherwise the modal edits this workload. */
		workload?: Workload | null;
		close: (result?: WorkloadModalResult) => void;
	} = $props();

	// The modal host mounts this component fresh per open, so seeding the
	// form from the initial prop value is exactly right.
	// svelte-ignore state_referenced_locally
	const editing = workload !== null;
	// svelte-ignore state_referenced_locally
	const initial = workload;

	let name = $state(initial?.name ?? '');
	let kind = $state<'application' | 'database'>(initial?.kind ?? 'application');
	let image = $state(initial?.image ?? '');
	let replicas = $state(initial?.replicas ?? 1);
	let pinnedNodes = $state<string[]>([...(initial?.constraints.node_ids ?? [])]);
	let envText = $state(
		Object.entries(initial?.spec.env ?? {})
			.map(([k, v]) => `${k}=${v}`)
			.join('\n')
	);
	let command = $state((initial?.spec.command ?? []).join(' '));
	let restartPolicy = $state(initial?.spec.restart_policy ?? '');
	let cpus = $state(initial?.spec.cpus ?? 0);
	let memoryMB = $state(Math.round((initial?.spec.memory_limit ?? 0) / (1024 * 1024)));
	let ports = $state<{ host: string; container: string; protocol: 'tcp' | 'udp' }[]>(
		(initial?.spec.ports ?? []).map((p) => ({
			host: p.host_port ? String(p.host_port) : '',
			container: String(p.container_port),
			protocol: p.protocol === 'udp' ? 'udp' : 'tcp'
		}))
	);
	let mounts = $state<{ type: 'volume' | 'bind'; source: string; target: string; ro: boolean }[]>(
		(initial?.spec.mounts ?? []).map((m) => ({
			type: m.type,
			source: m.source,
			target: m.target,
			ro: m.read_only ?? false
		}))
	);

	function togglePin(id: string) {
		pinnedNodes = pinnedNodes.includes(id)
			? pinnedNodes.filter((n) => n !== id)
			: [...pinnedNodes, id];
	}

	const nameValid = $derived(/^[a-z0-9][a-z0-9-]{0,53}$/.test(name));
	const portsValid = $derived(ports.every((p) => /^\d+$/.test(p.container)));
	const mountsValid = $derived(mounts.every((m) => m.source.trim() !== '' && m.target.trim() !== ''));
	const envValid = $derived(
		envText
			.split('\n')
			.filter((l) => l.trim() !== '')
			.every((l) => /^[^=\s]+=/.test(l))
	);
	const valid = $derived(
		nameValid && image.trim() !== '' && replicas >= 0 && portsValid && mountsValid && envValid
	);

	function buildSpec(): WorkloadSpec {
		const spec: WorkloadSpec = {};
		const env: Record<string, string> = {};
		for (const line of envText.split('\n')) {
			const trimmed = line.trim();
			if (trimmed === '') continue;
			const eq = trimmed.indexOf('=');
			env[trimmed.slice(0, eq)] = trimmed.slice(eq + 1);
		}
		if (Object.keys(env).length > 0) spec.env = env;
		if (command.trim() !== '') spec.command = command.trim().split(/\s+/);
		if (ports.length > 0) {
			spec.ports = ports.map((p) => ({
				host_port: p.host === '' ? 0 : Number(p.host),
				container_port: Number(p.container),
				protocol: p.protocol
			}));
		}
		if (mounts.length > 0) {
			spec.mounts = mounts.map((m) => ({
				type: m.type,
				source: m.source.trim(),
				target: m.target.trim(),
				read_only: m.ro
			}));
		}
		if (restartPolicy !== '') spec.restart_policy = restartPolicy;
		if (cpus > 0) spec.cpus = cpus;
		if (memoryMB > 0) spec.memory_limit = memoryMB * 1024 * 1024;
		// Networks, labels, and healthchecks stay API-only in v1.
		if (editing) {
			if (initial?.spec.networks?.length) spec.networks = initial.spec.networks;
			if (initial?.spec.labels && Object.keys(initial.spec.labels).length > 0)
				spec.labels = initial.spec.labels;
			if (initial?.spec.healthcheck) spec.healthcheck = initial.spec.healthcheck;
			if (initial?.spec.restart_max_retries)
				spec.restart_max_retries = initial.spec.restart_max_retries;
		}
		return spec;
	}

	function submit() {
		if (!valid) return;
		const body: Record<string, unknown> = {
			image: image.trim(),
			replicas,
			constraints: { node_ids: pinnedNodes },
			spec: buildSpec()
		};
		if (!editing) {
			body.name = name.trim();
			body.kind = kind;
		}
		close({ body });
	}

	const inputClass =
		'border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-full rounded-[10px] border px-3.25 py-2.5 text-[13px] transition-colors focus:outline-none';
	const smallInputClass =
		'border-border-strong bg-surface-input text-text-primary focus:border-accent/50 rounded-[10px] border px-2.5 py-2 font-mono text-[12.5px] transition-colors focus:outline-none';
</script>

<ModalHeader
	title={editing ? `Edit ${initial?.name}` : 'New workload'}
	description={editing
		? 'Changes bump the generation; the reconciler replaces replicas whose spec drifted.'
		: 'Declare the desired state — the reconciler imports the image into the mirror, places the replicas, and keeps them converged.'}
/>

<form
	class="flex max-h-[60vh] flex-col gap-3.5 overflow-y-auto px-5.5 py-4"
	onsubmit={(e) => {
		e.preventDefault();
		submit();
	}}
>
	{#if !editing}
		<div class="grid grid-cols-[1.6fr_1fr] gap-3">
			<label class="flex flex-col gap-1.5">
				<span class="text-text-tertiary text-[12.5px] font-medium">Name</span>
				<input
					bind:value={name}
					type="text"
					required
					placeholder="blog"
					class="{inputClass} font-mono {name !== '' && !nameValid ? 'border-status-danger/60' : ''}"
				/>
			</label>
			<div class="flex flex-col gap-1.5">
				<span class="text-text-tertiary text-[12.5px] font-medium">Kind</span>
				<div class="flex gap-2">
					{#each ['application', 'database'] as const as k (k)}
						<button
							type="button"
							onclick={() => (kind = k)}
							class="flex-1 cursor-pointer rounded-[10px] border px-2 py-2.25 text-[12.5px] font-medium transition-colors {kind ===
							k
								? 'border-accent/50 bg-accent/10 text-accent-nav'
								: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
						>
							{k}
						</button>
					{/each}
				</div>
			</div>
		</div>
		<p class="text-text-ghost -mt-2 text-[11.5px] leading-relaxed">
			Lowercase letters, digits, dashes — replicas run as <span class="font-mono"
				>{nameValid ? name : '<name>'}-0…{Math.max(replicas - 1, 0)}</span
			>.
		</p>
	{/if}

	<div class="grid grid-cols-[1.6fr_1fr] gap-3">
		<label class="flex flex-col gap-1.5">
			<span class="text-text-tertiary text-[12.5px] font-medium">Image</span>
			<input
				bind:value={image}
				type="text"
				required
				placeholder="ghcr.io/acme/blog:1.2"
				class="{inputClass} font-mono"
			/>
		</label>
		<label class="flex flex-col gap-1.5">
			<span class="text-text-tertiary text-[12.5px] font-medium">Replicas</span>
			<input bind:value={replicas} type="number" min="0" max="99" class={inputClass} />
		</label>
	</div>
	<p class="text-text-ghost -mt-2 text-[11.5px] leading-relaxed">
		Upstream tag reference — imported into the cluster mirror digest-pinned; nodes pull from the
		LAN. Replicas land on distinct nodes.
	</p>

	<div class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Nodes</span>
		<div class="flex flex-wrap gap-2">
			{#each nodes as n (n.id)}
				{@const active = pinnedNodes.includes(n.id)}
				<button
					type="button"
					onclick={() => togglePin(n.id)}
					class="cursor-pointer rounded-[10px] border px-3 py-2 text-[12.5px] font-medium transition-colors {active
						? 'border-accent/50 bg-accent/10 text-accent-nav'
						: 'border-border-strong text-text-tertiary hover:bg-white/4'}"
				>
					{n.name}
				</button>
			{/each}
		</div>
		<p class="text-text-ghost text-[11.5px] leading-relaxed">
			{pinnedNodes.length === 0
				? 'No pins: the reconciler places replicas on any node.'
				: 'Replicas may only land on the pinned nodes.'}
		</p>
	</div>

	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Environment</span>
		<textarea
			bind:value={envText}
			rows="3"
			placeholder={'DATABASE_URL=postgres://…\nLOG_LEVEL=info'}
			class="{inputClass} resize-y font-mono {envText !== '' && !envValid
				? 'border-status-danger/60'
				: ''}"
		></textarea>
	</label>

	<div class="flex flex-col gap-1.5">
		<div class="flex items-center justify-between">
			<span class="text-text-tertiary text-[12.5px] font-medium">Ports</span>
			<button
				type="button"
				onclick={() => (ports = [...ports, { host: '', container: '', protocol: 'tcp' }])}
				class="text-text-ghost hover:text-text-primary flex cursor-pointer items-center gap-1 text-[11.5px] transition-colors"
			>
				<Plus size={12} /> Add port
			</button>
		</div>
		{#each ports as p, i (i)}
			<div class="flex items-center gap-2">
				<input
					bind:value={p.host}
					type="text"
					placeholder="host (auto)"
					class="{smallInputClass} w-28"
				/>
				<span class="text-text-ghost">→</span>
				<input
					bind:value={p.container}
					type="text"
					placeholder="container"
					class="{smallInputClass} w-28"
				/>
				<select bind:value={p.protocol} class={smallInputClass}>
					<option value="tcp">tcp</option>
					<option value="udp">udp</option>
				</select>
				<button
					type="button"
					onclick={() => (ports = ports.filter((_, j) => j !== i))}
					class="text-text-ghost hover:text-status-danger cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5"
					aria-label="Remove port mapping"
				>
					<X size={13} />
				</button>
			</div>
		{/each}
	</div>

	<div class="flex flex-col gap-1.5">
		<div class="flex items-center justify-between">
			<span class="text-text-tertiary text-[12.5px] font-medium">Volumes</span>
			<button
				type="button"
				onclick={() => (mounts = [...mounts, { type: 'volume', source: '', target: '', ro: false }])}
				class="text-text-ghost hover:text-text-primary flex cursor-pointer items-center gap-1 text-[11.5px] transition-colors"
			>
				<Plus size={12} /> Add mount
			</button>
		</div>
		{#each mounts as m, i (i)}
			<div class="flex items-center gap-2">
				<select bind:value={m.type} class={smallInputClass}>
					<option value="volume">volume</option>
					<option value="bind">bind</option>
				</select>
				<input
					bind:value={m.source}
					type="text"
					placeholder={m.type === 'volume' ? 'data' : '/host/path'}
					class="{smallInputClass} flex-1"
				/>
				<span class="text-text-ghost">→</span>
				<input bind:value={m.target} type="text" placeholder="/data" class="{smallInputClass} flex-1" />
				<label class="text-text-ghost flex items-center gap-1 text-[11px]">
					<input bind:checked={m.ro} type="checkbox" class="accent-(--color-accent)" /> ro
				</label>
				<button
					type="button"
					onclick={() => (mounts = mounts.filter((_, j) => j !== i))}
					class="text-text-ghost hover:text-status-danger cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5"
					aria-label="Remove mount"
				>
					<X size={13} />
				</button>
			</div>
		{/each}
	</div>

	<div class="grid grid-cols-3 gap-3">
		<label class="flex flex-col gap-1.5">
			<span class="text-text-tertiary text-[12.5px] font-medium">Command</span>
			<input
				bind:value={command}
				type="text"
				placeholder="image default"
				class="{inputClass} font-mono"
			/>
		</label>
		<label class="flex flex-col gap-1.5">
			<span class="text-text-tertiary text-[12.5px] font-medium">CPUs</span>
			<input
				bind:value={cpus}
				type="number"
				min="0"
				step="0.1"
				placeholder="unlimited"
				class={inputClass}
			/>
		</label>
		<label class="flex flex-col gap-1.5">
			<span class="text-text-tertiary text-[12.5px] font-medium">Memory (MB)</span>
			<input
				bind:value={memoryMB}
				type="number"
				min="0"
				step="64"
				placeholder="unlimited"
				class={inputClass}
			/>
		</label>
	</div>

	<label class="flex flex-col gap-1.5">
		<span class="text-text-tertiary text-[12.5px] font-medium">Restart policy</span>
		<select bind:value={restartPolicy} class={inputClass}>
			<option value="">unless-stopped (default)</option>
			<option value="always">always</option>
			<option value="on-failure">on-failure</option>
			<option value="no">no</option>
		</select>
	</label>

	<!-- Hidden submit so Enter works; the visible buttons live in the footer. -->
	<button type="submit" class="hidden" aria-hidden="true"></button>
</form>

<div class="border-border-subtle bg-surface-raised/50 flex justify-end gap-2 border-t px-5.5 py-3">
	<Button variant="ghost" onclick={() => close(undefined)}>Cancel</Button>
	<Button variant="primary" disabled={!valid} onclick={submit}>
		{editing ? 'Save changes' : 'Create workload'}
	</Button>
</div>
