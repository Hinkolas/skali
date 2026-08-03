<script lang="ts">
	import type { ApplicationService } from '$lib/mock/types';
	import { dialog } from '$lib/stores/dialog.svelte';
	import { toast } from '$lib/stores/toast.svelte';
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import KeyValueRow from '$lib/components/ui/KeyValueRow.svelte';

	let { service }: { service: ApplicationService } = $props();

	function restart() {
		dialog.confirm({
			title: `Restart ${service.name}?`,
			description: 'Instances are restarted one by one so the service stays reachable.',
			confirmLabel: 'Restart',
			onConfirm: () => {
				void toast.promise(new Promise((resolve) => setTimeout(resolve, 1400)), {
					loading: `Restarting ${service.name}…`,
					success: `${service.name} restarted`,
					error: 'Restart failed'
				});
			}
		});
	}
</script>

<Card class="p-5">
	<div class="mb-3.5 flex items-center gap-2.5">
		<h3 class="text-text-primary text-xl font-semibold">Web process</h3>
		<span class="text-status-success flex items-center gap-1.5 text-md">
			<span class="bg-status-success size-[8px] rounded-full"></span>healthy
		</span>
		<div class="ml-auto flex gap-2">
			<Button size="sm" onclick={restart}>Restart</Button>
			<Button size="sm" onclick={() => toast.info('Shell access is coming soon')}>Shell</Button>
		</div>
	</div>
	<div class="flex flex-col">
		<KeyValueRow k="Image" v={service.image} />
		<KeyValueRow k="Start command" v={service.start_command} />
		<KeyValueRow k="Port" v={service.port} />
		<KeyValueRow k="Instances" v="{service.instances} · {service.instance_nodes}" />
		<KeyValueRow k="Health check" v={service.health_check} />
	</div>
</Card>
