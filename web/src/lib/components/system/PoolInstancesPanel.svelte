<script lang="ts">
	import { formatBytes, formatDateTime } from '$lib/format';
	import {
		POOL_CLASS_TEXT,
		memberSummary,
		poolBudgetText,
		poolPhase,
		type DatabasePoolDetail
	} from '$lib/types/pools';
	import Card from '$lib/components/ui/Card.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';
	import PoolMemberList from './PoolMemberList.svelte';

	// The server behind one pool: what it runs, how it is sized, and the
	// instances carrying it.
	let { pool }: { pool: DatabasePoolDetail } = $props();

	const phase = $derived(poolPhase(pool));
	const dot: Record<string, string> = {
		success: 'bg-status-success',
		warning: 'bg-status-warning',
		neutral: 'bg-text-ghost'
	};
	const instances = $derived.by(() => {
		const declared = `${pool.instances} declared`;
		if (!pool.observed) return `${declared} · not observed`;
		return `${declared} · ${pool.observed.ready_instances}/${pool.observed.instances} ready`;
	});
</script>

<Card class="flex flex-col p-5">
	<div class="mb-3.5 flex items-center gap-2.5">
		<h3 class="text-text-primary text-xl font-semibold">Instances</h3>
		<span class="text-text-muted flex items-center gap-1.5 text-md">
			<span class="size-[8px] rounded-full {dot[phase.tone]}"></span>{phase.text}
		</span>
	</div>
	<div class="flex flex-col">
		<KeyValueRow k="Engine" v="{pool.engine} {pool.major}" labelWidth="w-36" />
		<KeyValueRow k="Image" v={pool.image || 'unknown'} labelWidth="w-36" />
		<KeyValueRow k="Class" v={POOL_CLASS_TEXT[pool.class] ?? pool.class} labelWidth="w-36" />
		<KeyValueRow k="Instances" v={instances} labelWidth="w-36" />
		<KeyValueRow k="Memory budget" v={poolBudgetText(pool)} labelWidth="w-36" />
		<KeyValueRow k="Storage" v="{formatBytes(pool.storage_bytes)} per instance" labelWidth="w-36" />
		<KeyValueRow
			k="Node port"
			v={pool.node_port == null ? 'none' : String(pool.node_port)}
			labelWidth="w-36"
		/>
		<KeyValueRow k="Created" v={formatDateTime(pool.created_at)} labelWidth="w-36" />
	</div>

	<div class="mt-5 mb-2.5 flex items-baseline gap-2.5">
		<h4 class="text-text-primary text-base font-semibold">Members</h4>
		<span class="font-mono text-text-faint text-xs">{memberSummary(pool.members)}</span>
	</div>
	<PoolMemberList members={pool.members} observed={pool.observed != null} />
</Card>
