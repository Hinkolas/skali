<script lang="ts">
	let { href, label }: { href: string; label: string } = $props();
</script>

<!-- eslint-disable svelte/no-navigation-without-resolve -- pass-through: callers hand in resolved hrefs -->
<a
	{href}
	class="text-text-faint hover:text-text-secondary flex items-center gap-1.5 px-1 pt-0.5 pb-2 text-[12px] transition-colors"
>
	<span class="opacity-70">←</span>
	{label}
</a>
