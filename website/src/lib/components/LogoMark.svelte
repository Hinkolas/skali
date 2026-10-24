<script lang="ts">
	import { asset } from '$app/paths';

	let { size = 26 }: { size?: number } = $props();
</script>

<!-- The same approved v5 tile used by the native app and Studio. -->
<img
	src={asset('/favicon.png')}
	alt=""
	width={size}
	height={size}
	class="block shrink-0"
	style:border-radius="{Math.round(size * 0.27)}px"
	aria-hidden="true"
/>
