<script lang="ts">
	import type { DatabaseService } from '$lib/mock/types';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';

	let { service }: { service: DatabaseService } = $props();

	function enablePublicAccess() {
		dialog.confirm({
			title: 'Enable public access?',
			description: `${service.name} would be reachable from the internet through a TLS endpoint. You can disable this at any time.`,
			confirmLabel: 'Enable',
			onConfirm: () => {
				toast.success('Public access enabled', {
					description: 'Mock only — the database stays private.'
				});
			}
		});
	}
</script>

<Card class="flex flex-col p-5">
	<div class="mb-4 flex items-center">
		<h3 class="text-text-primary text-[15px] font-semibold">External connection</h3>
		<div class="ml-auto">
			<Button size="sm" onclick={enablePublicAccess}>Enable public access</Button>
		</div>
	</div>
	<div
		class="border-border-strong grid min-h-[180px] flex-1 place-items-center rounded-[10px] border border-dashed"
	>
		<div class="flex flex-col gap-2 p-5 text-center">
			<div class="text-text-muted text-[13px]">Public access is disabled</div>
			<div class="font-mono text-text-ghost text-[10.5px]">
				reachable only inside the cluster network
			</div>
		</div>
	</div>
</Card>
