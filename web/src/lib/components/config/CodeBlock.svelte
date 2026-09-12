<script lang="ts">
	// A command as it would be typed: a prompt glyph and the argv joined.
	// `muted` marks a placeholder such as the image default.
	let { command, muted = false }: { command: string; muted?: boolean } = $props();
</script>

<div
	class="bg-surface-input border-border-default flex items-baseline gap-2 overflow-x-auto rounded-[9px] border px-3 py-2 font-mono text-md whitespace-nowrap"
>
	<span class="text-text-ghost select-none">$</span>
	<span class={muted ? 'text-text-faint italic' : 'text-text-secondary'}>{command}</span>
</div>
