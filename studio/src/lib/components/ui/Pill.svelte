<script lang="ts">
	// Small mono pill: environment names, value flags. production = green.
	let { text, tone = 'neutral' }: { text: string; tone?: 'success' | 'warning' | 'neutral' } =
		$props();

	const toneClass: Record<string, string> = {
		success: 'text-status-success bg-status-success/10',
		warning: 'text-status-warning bg-status-warning/10',
		neutral: 'text-text-muted bg-white/6'
	};
</script>

<span class="font-mono rounded-full px-2 py-0.5 text-2xs {toneClass[tone]}">
	{text}
</span>
