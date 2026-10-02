<script lang="ts">
	import './layout.css';

	let { children } = $props();
</script>

<!-- At least one window tall, so a short page's footer sits at the bottom
     rather than in the middle of the screen; pages let their <main> grow. -->
<div class="flex min-h-dvh flex-col">
	{@render children()}
</div>
