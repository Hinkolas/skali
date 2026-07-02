<script lang="ts">
	import { setUser } from '$lib/stores/auth.svelte';
	import type { LayoutData } from './$types';

	let { data, children }: { data: LayoutData; children: import('svelte').Snippet } = $props();

	$effect.pre(() => {
		setUser(data.user);
	});
</script>

{@render children()}
