<script lang="ts">
	// Environment pill on project cards: production = green tint, rest neutral.
	let { text, tone = 'neutral' }: { text: string; tone?: 'success' | 'neutral' } = $props();
</script>

<span
	class="font-mono rounded-full px-2 py-0.5 text-2xs {tone === 'success'
		? 'text-status-success bg-status-success/10'
		: 'text-text-muted bg-white/6'}"
>
	{text}
</span>
