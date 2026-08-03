<script lang="ts">
	let { text, ok = true }: { text: string; ok?: boolean } = $props();
</script>

<div class="font-mono text-text-faint flex items-center gap-1.75 px-1 pb-2.5 text-xs">
	<span class="size-1.5 flex-none rounded-full {ok ? 'bg-status-success' : 'bg-status-warning'}"
	></span>
	{text}
</div>
