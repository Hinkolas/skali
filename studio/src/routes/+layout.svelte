<script lang="ts">
	import './layout.css';
	import favicon from '$lib/assets/logo.png';
	import Modal from '$lib/components/ui/Modal.svelte';
	import Toaster from '$lib/components/ui/Toaster.svelte';

	let { children } = $props();
</script>

<svelte:head>
	<title>Skali</title>
	<link rel="icon" href={favicon} />
</svelte:head>

{@render children()}

<Toaster />
<Modal />
