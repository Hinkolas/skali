<script lang="ts">
	type Tone = 'success' | 'warning' | 'neutral';
	let { text, tone = 'success' }: { text: string; tone?: Tone } = $props();

	const DOT: Record<Tone, string> = {
		success: 'bg-status-success',
		warning: 'bg-status-warning',
		neutral: 'bg-text-faint'
	};
</script>

<div class="font-mono text-text-faint flex items-center gap-1.75 px-1 pb-2.5 text-xs">
	<span class="size-1.5 flex-none rounded-full {DOT[tone]}"></span>
	{text}
</div>
