<script lang="ts">
	let {
		k,
		v,
		labelWidth = 'w-30'
	}: {
		k: string;
		v: string;
		labelWidth?: string;
	} = $props();
</script>

<div class="border-border-subtle flex items-baseline gap-3 border-b py-2 last:border-0">
	<div class="text-text-faint flex-none text-[12px] {labelWidth}">{k}</div>
	<div
		class="font-mono text-text-secondary min-w-0 overflow-hidden text-[11.5px] text-ellipsis whitespace-nowrap"
	>
		{v}
	</div>
</div>
