<script lang="ts">
	let {
		pct,
		class: fillClass = 'bg-accent'
	}: {
		pct: number;
		class?: string;
	} = $props();
</script>

<div class="h-1 rounded-full bg-white/6">
	<div class="h-full rounded-full {fillClass}" style:width="{pct}%"></div>
</div>
