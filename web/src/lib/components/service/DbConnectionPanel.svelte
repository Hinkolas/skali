<script lang="ts">
	import type { DatabaseService } from '$lib/mock/types';
	import Card from '$lib/components/ui/Card.svelte';
	import CopyField from '$lib/components/ui/CopyField.svelte';

	let { service }: { service: DatabaseService } = $props();
</script>

<Card class="p-5">
	<div class="mb-4 flex items-center gap-2.5">
		<h3 class="text-text-primary text-[15px] font-semibold">Internal connection</h3>
		<span
			class="font-mono text-status-success bg-status-success/10 rounded-full px-2 py-0.5 text-[9.5px]"
		>
			private network
		</span>
	</div>
	<div class="flex flex-col gap-2.25">
		<CopyField label="Host" value={service.connection.host} />
		<CopyField label="Port" value={service.connection.port} />
		<CopyField label="Database" value={service.connection.database} />
		<CopyField label="User" value={service.connection.user} />
		<CopyField label="Password" value={service.connection.password} masked />
		<CopyField label="URL" value={service.connection.url} masked />
	</div>
</Card>
