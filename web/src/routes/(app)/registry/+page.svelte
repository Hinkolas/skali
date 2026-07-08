<script lang="ts">
	import Plus from '@lucide/svelte/icons/plus';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import Copy from '@lucide/svelte/icons/copy';
	import Search from '@lucide/svelte/icons/search';
	import LoaderCircle from '@lucide/svelte/icons/loader-circle';
	import CircleAlert from '@lucide/svelte/icons/circle-alert';
	import { invalidateAll } from '$app/navigation';
	import { api, ApiError } from '$lib/api/client';
	import { modal } from '$lib/stores/modal.svelte';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import { formatBytes, relativeTime } from '$lib/format';
	import type { RegistryImage } from '$lib/types/registry';
	import type { Operation } from '$lib/types/operations';
	import PageHeader from '$lib/components/shell/PageHeader.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Table from '$lib/components/ui/Table.svelte';
	import EmptyState from '$lib/components/ui/EmptyState.svelte';
	import ImportImageModal, {
		modalOptions as importImageOptions,
		type ImportImageResult
	} from '$lib/components/registry/ImportImageModal.svelte';
	import type { PageData } from './$types';

	let { data }: { data: PageData } = $props();

	const grid = 'grid-cols-[2.6fr_0.7fr_1.5fr_0.8fr_0.9fr_56px]';

	// Imports run as background operations (202): running ones render as
	// in-flight rows, failures stick around with their error until a newer
	// import of the same reference succeeds (or the server prunes them).
	const runningImports = $derived(data.operations.filter((o) => o.status === 'running'));
	const failedImports = $derived(
		data.operations
			.filter(
				(o) =>
					o.status === 'failed' &&
					!data.operations.some(
						(s) =>
							s.subject === o.subject && s.status === 'succeeded' && s.created_at > o.created_at
					)
			)
			.slice(0, 3)
	);

	// Live-ish state: re-run the server load every 10s while the page is open
	// (an import from another session shows up without a reload); tighten to
	// 3s while an import is in flight so the finished row appears promptly.
	$effect(() => {
		const t = setInterval(() => invalidateAll(), runningImports.length > 0 ? 3_000 : 10_000);
		return () => clearInterval(t);
	});

	let query = $state('');
	const q = $derived(query.trim().toLowerCase());
	const images = $derived(
		data.images.filter(
			(i) =>
				!q ||
				i.repository.toLowerCase().includes(q) ||
				i.tag.toLowerCase().includes(q) ||
				i.digest.includes(q)
		)
	);

	/** Display name without the mirror/ ownership prefix noise. */
	function displayName(img: RegistryImage): string {
		return img.repository.replace(/^mirror\//, '');
	}
	function shortDigest(img: RegistryImage): string {
		return img.digest.replace('sha256:', '').slice(0, 12);
	}

	async function copyDigest(img: RegistryImage) {
		try {
			await navigator.clipboard.writeText(img.digest);
			toast.success('Digest copied');
		} catch {
			toast.error('Could not access the clipboard');
		}
	}

	// The sudo-gated import call runs BETWEEN modal close and any toast —
	// never while the modal is open — so the reauth modal slot stays free.
	let importing = $state(false);

	async function importImage() {
		const result = await modal.open<ImportImageResult>(ImportImageModal, {}, importImageOptions)
			.result;
		if (!result) return;
		importing = true;
		try {
			await api.post<{ operation: Operation }>('/v1/registry/images', {
				reference: result.reference
			});
			toast.success(`Import of ${result.reference} started`);
			await invalidateAll();
		} catch (err) {
			toast.error(err instanceof ApiError ? err.message : `Could not import ${result.reference}`);
		} finally {
			importing = false;
		}
	}

	function removeImage(img: RegistryImage) {
		dialog.confirm({
			title: `Remove ${displayName(img)}:${img.tag}?`,
			description:
				'Nodes can no longer pull it from the mirror; running containers are untouched. It can always be imported again.',
			confirmLabel: 'Remove from mirror',
			variant: 'danger',
			onConfirm: async () => {
				try {
					await api.del(`/v1/registry/images/${img.id}`);
					toast.success(`Removed ${displayName(img)}:${img.tag}`);
					await invalidateAll();
				} catch (err) {
					toast.error(err instanceof ApiError ? err.message : 'Could not remove the image');
					throw err; // keep the dialog open
				}
			}
		});
	}
</script>

<svelte:head>
	<title>Registry — skali</title>
</svelte:head>

<PageHeader title="Registry">
	{#snippet subtitle()}
		{#if data.disabled}
			The cluster image mirror is disabled
		{:else}
			{data.images.length} image{data.images.length === 1 ? '' : 's'} in the cluster mirror
		{/if}
	{/snippet}
	{#snippet actions()}
		{#if !data.disabled}
			<Button variant="primary" busy={importing} onclick={importImage}>
				<Plus size={15} strokeWidth={2.5} />
				Import image
			</Button>
		{/if}
	{/snippet}
</PageHeader>

{#if data.disabled}
	<EmptyState
		title="No registry on this master"
		description="The image mirror follows the cluster address: set CLUSTER_ADDR on the master and restart skalid to run the registry."
	/>
{:else}
	<div class="flex flex-wrap items-center gap-2.5 pb-4">
		<div class="ml-auto flex items-center gap-2">
			<label class="relative">
				<Search size={13} class="text-text-ghost absolute top-1/2 left-3 -translate-y-1/2" />
				<input
					bind:value={query}
					type="text"
					placeholder="Filter…"
					class="border-border-strong bg-surface-input text-text-primary focus:border-accent/50 w-44 rounded-[10px] border py-2 pr-3 pl-8.5 text-[12.5px] transition-colors focus:outline-none"
				/>
			</label>
		</div>
	</div>

	{#if runningImports.length > 0 || failedImports.length > 0}
		<div class="flex flex-col gap-1.5 pb-4">
			{#each runningImports as op (op.id)}
				<div
					class="border-border-subtle bg-surface-raised flex items-center gap-2.5 rounded-[10px] border px-4 py-2.5"
				>
					<LoaderCircle size={14} class="text-accent animate-spin" />
					<span class="text-text-primary text-[12.5px]">
						Importing <span class="font-mono font-medium">{op.subject}</span>…
					</span>
					<span class="text-text-ghost ml-auto font-mono text-[11px]" title={op.created_at}>
						started {relativeTime(op.created_at)}
					</span>
				</div>
			{/each}
			{#each failedImports as op (op.id)}
				<div
					class="border-status-danger/25 bg-status-danger/5 flex items-center gap-2.5 rounded-[10px] border px-4 py-2.5"
				>
					<CircleAlert size={14} class="text-status-danger shrink-0" />
					<span class="text-text-primary min-w-0 truncate text-[12.5px]">
						Import of <span class="font-mono font-medium">{op.subject}</span> failed{op.error
							? `: ${op.error}`
							: ''}
					</span>
					<span
						class="text-text-ghost ml-auto shrink-0 font-mono text-[11px]"
						title={op.finished_at}
					>
						{relativeTime(op.finished_at ?? op.updated_at)}
					</span>
				</div>
			{/each}
		</div>
	{/if}

	{#if images.length === 0}
		<EmptyState
			title={q ? 'No matching images' : 'The mirror is empty'}
			description={q
				? 'Nothing matches the current search.'
				: 'Import an upstream image and every node pulls it from the LAN, digest-pinned — no public egress needed on workers.'}
		/>
	{:else}
		<Table columns={['Image', 'Tag', 'Digest', 'Size', 'Imported', '']} {grid}>
			{#each images as img (img.id)}
				<div
					class="border-border-subtle grid items-center border-b px-4.5 py-3 transition-colors last:border-0 hover:bg-white/2 {grid}"
				>
					<div class="flex min-w-0 flex-col gap-px">
						<span class="font-mono text-text-primary truncate text-[12.5px] font-medium">
							{displayName(img)}
						</span>
						<span class="font-mono text-text-faint truncate text-[11px]">{img.repository}</span>
					</div>
					<div class="font-mono text-text-muted truncate text-[12px]">{img.tag}</div>
					<div>
						<button
							type="button"
							onclick={() => copyDigest(img)}
							class="text-text-muted hover:text-text-primary group flex cursor-pointer items-center gap-1.5 font-mono text-[11.5px] transition-colors"
							title="Copy full digest"
						>
							{shortDigest(img)}
							<Copy size={11} class="opacity-0 transition-opacity group-hover:opacity-100" />
						</button>
					</div>
					<div class="font-mono text-text-muted text-[11.5px]">
						{img.size_bytes > 0 ? formatBytes(img.size_bytes) : '—'}
					</div>
					<div class="font-mono text-text-muted text-[11px]" title={img.updated_at}>
						{relativeTime(img.updated_at)}
					</div>
					<div class="flex items-center justify-end">
						<button
							type="button"
							onclick={() => removeImage(img)}
							class="text-text-ghost hover:text-status-danger cursor-pointer rounded-lg p-1.5 transition-colors hover:bg-white/5"
							aria-label="Remove {displayName(img)}:{img.tag}"
							title="Remove"
						>
							<Trash2 size={14} />
						</button>
					</div>
				</div>
			{/each}
		</Table>
		<p class="text-text-ghost mt-3 text-[11px] leading-relaxed">
			Nodes pull these as <span class="font-mono"
				>&lt;registry&gt;/&lt;repository&gt;:&lt;tag&gt;</span
			> over cluster mTLS. Removing an entry deletes its manifest; disk is reclaimed by a later garbage-collection
			milestone.
		</p>
	{/if}
{/if}
