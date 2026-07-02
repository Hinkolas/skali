<script lang="ts">
	import type { ChipTone } from '$lib/mock/types';

	let { text, tone = 'neutral' }: { text: string; tone?: ChipTone } = $props();

	const toneClass: Record<ChipTone, string> = {
		success: 'text-status-success bg-status-success/10',
		neutral: 'text-text-muted bg-white/5',
		warning: 'text-status-warning bg-status-warning/10'
	};
</script>

<span class="font-mono rounded-md px-1.5 py-0.5 text-[10px] {toneClass[tone]}">{text}</span>
