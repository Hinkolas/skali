<script lang="ts">
	import { page } from '$app/state';
	import ErrorState from '$lib/components/ui/ErrorState.svelte';
</script>

<svelte:head>
	<title>{page.status} — skali</title>
</svelte:head>

<!-- pb-16 lifts the block slightly above true center, which reads better
     inside the tall main column. -->
<div class="grid h-full place-items-center pb-16">
	<ErrorState />
</div>
