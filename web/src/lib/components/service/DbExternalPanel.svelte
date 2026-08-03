<script lang="ts">
	import Button from '$lib/components/ui/Button.svelte';
	import Card from '$lib/components/ui/Card.svelte';
</script>

<Card class="flex flex-col p-5">
	<div class="mb-4 flex items-center">
		<h3 class="text-text-primary text-xl font-semibold">External connection</h3>
		<div class="ml-auto">
			<span title="Public database access is coming soon">
				<Button size="sm" disabled>Enable public access</Button>
			</span>
		</div>
	</div>
	<div
		class="border-border-strong grid min-h-[198px] flex-1 place-items-center rounded-[11px] border border-dashed"
	>
		<div class="flex flex-col gap-2 p-5 text-center">
			<div class="text-text-muted text-base">Public access is disabled</div>
			<div class="font-mono text-text-ghost text-xs">reachable only inside the cluster network</div>
		</div>
	</div>
</Card>
