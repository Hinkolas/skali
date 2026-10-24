<script lang="ts">
	// The approved v5 app icon, resized for web use. Callers size the full tile.
	import logo from '$lib/assets/logo.png';

	let { class: cls = '' }: { class?: string } = $props();
</script>

<img src={logo} alt="" class={`block shrink-0 ${cls}`} aria-hidden="true" />
